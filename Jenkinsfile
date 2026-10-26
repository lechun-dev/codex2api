pipeline {
  agent any

  options {
    timestamps()
    disableConcurrentBuilds()
    buildDiscarder(logRotator(numToKeepStr: '20'))
  }

  parameters {
    choice(name: 'GOARCH', choices: ['amd64', 'arm64'], description: 'Linux target architecture')
    string(name: 'SSH_CREDENTIALS_ID', defaultValue: 'codex2api-prod-ssh', description: 'Jenkins SSH credential ID for the deployment server')
    string(name: 'DEPLOY_HOST', defaultValue: 'your-server.example.com', description: 'Deployment server hostname or IP')
    string(name: 'DEPLOY_USER', defaultValue: 'appuser', description: 'SSH user on the deployment server')
    string(name: 'DEPLOY_DIR', defaultValue: '/opt/codex2api', description: 'Application directory on the deployment server')
    string(name: 'SERVICE_NAME', defaultValue: 'codex2api', description: 'systemd service name')
    string(name: 'HEALTHCHECK_URL', defaultValue: '', description: 'Optional URL checked after restart')
    booleanParam(name: 'RUN_MYSQL_COMPAT', defaultValue: true, description: 'Run the MySQL 5.6 compatibility gate before building (fork-only requirement)')
    string(name: 'MYSQL_COMPAT_IMAGE', defaultValue: 'mysql:5.6', description: 'Docker image used by the MySQL 5.6 compatibility gate')
    string(name: 'RELEASES_TO_KEEP', defaultValue: '5', description: 'How many release directories to keep on the server')
  }

  tools {
    nodejs 'Node24'
  }

  environment {
    APP_NAME = 'codex2api'
    PATH = "/usr/local/go/bin:${env.PATH}"
  }

  stages {
    stage('Checkout') {
      steps {
        checkout scm
      }
    }

    stage('Frontend') {
      steps {
        dir('frontend') {
          sh 'npm ci'
          sh 'VITE_APP_VERSION="${BUILD_NUMBER:-jenkins}" npm run build'
          sh 'npm run typecheck'
        }
      }
    }

    stage('Test') {
      steps {
        sh 'go test ./...'
      }
    }

    // 上游只保证 PostgreSQL/SQLite；MySQL 5.6 由 driver 层重写 + isMySQL() 分支维持。
    // 没有真实 5.6 时 mysql_integration_test.go 会 Skip，发布链路等于没验证，
    // 所以这里必须起一个真 5.6 跑集成测试。
    stage('MySQL 5.6 兼容') {
      when {
        expression { return params.RUN_MYSQL_COMPAT }
      }
      steps {
        sh '''
          set -eu
          if ! command -v docker >/dev/null 2>&1; then
            echo "MySQL 5.6 compatibility gate needs docker on the agent." >&2
            echo "If this agent has no docker, point CODEX2API_MYSQL_TEST_DSN at a dedicated 5.6 instance and rerun with RUN_MYSQL_COMPAT enabled." >&2
            exit 1
          fi
          cid=$(docker run -d -e MYSQL_ROOT_PASSWORD=test-only -e MYSQL_DATABASE=codex2api_test -p 13306:3306 "${MYSQL_COMPAT_IMAGE}")
          cleanup() { docker rm -f "$cid" >/dev/null 2>&1 || true; }
          trap cleanup EXIT
          ready=0
          for i in $(seq 1 90); do
            if docker exec "$cid" mysqladmin ping -ptest-only --silent >/dev/null 2>&1; then
              ready=1
              break
            fi
            sleep 2
          done
          if [ "$ready" != "1" ]; then
            echo "MySQL 5.6 container never became ready" >&2
            docker logs "$cid" >&2 || true
            exit 1
          fi
          export CODEX2API_MYSQL_TEST_DSN="root:test-only@tcp(127.0.0.1:13306)/codex2api_test?parseTime=true&charset=utf8&loc=UTC"
          export CODEX2API_TEST_MYSQL_DSN="$CODEX2API_MYSQL_TEST_DSN"
          go test -count=1 ./database -run 'TestMySQL.*Integration|TestAPIKeyAuthRevisionMySQL'
        '''
      }
    }

    stage('Build') {
      steps {
        sh '''
          set -eu
          bin_path="build/codex2api-linux-${GOARCH}"
          mkdir -p build
          CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" go build -trimpath -ldflags="-s -w" -o "$bin_path" .
          file "$bin_path"
        '''
      }
    }

    stage('Deploy') {
      steps {
        sshagent(credentials: [params.SSH_CREDENTIALS_ID]) {
          sh '''
            set -eu
            remote="${DEPLOY_USER}@${DEPLOY_HOST}"
            release="${DEPLOY_DIR}/releases/${BUILD_NUMBER}"
            bin_path="build/codex2api-linux-${GOARCH}"

            ssh -o StrictHostKeyChecking=accept-new "$remote" "mkdir -p '$release' '${DEPLOY_DIR}/shared'"
            scp "$bin_path" "$remote:$release/codex2api"
            ssh "$remote" "
              set -eu
              test -f '${DEPLOY_DIR}/shared/.env'
              chmod +x '$release/codex2api'
              ln -sfn '$release' '${DEPLOY_DIR}/current'
              sudo systemctl restart '${SERVICE_NAME}'
              sudo systemctl --no-pager --full status '${SERVICE_NAME}' >/dev/null
              # 每个 release 目录都会留下二进制与本次运行日志（审计日志默认写在
              # 工作目录下），不清理会无限增长；current 始终指向最新的一个。
              ls -1dt '${DEPLOY_DIR}/releases'/*/ | tail -n +$((RELEASES_TO_KEEP + 1)) | xargs -r rm -rf
            "
          '''
        }
      }
    }

    stage('Healthcheck') {
      when {
        expression { return params.HEALTHCHECK_URL?.trim() }
      }
      steps {
        sh '''
          set -eu
          for i in $(seq 1 30); do
            if curl -fsS --max-time 5 "${HEALTHCHECK_URL}" >/dev/null; then
              exit 0
            fi
            sleep 2
          done
          echo "healthcheck failed: ${HEALTHCHECK_URL}" >&2
          exit 1
        '''
      }
    }
  }

  post {
    always {
      archiveArtifacts artifacts: 'build/codex2api-linux-*', fingerprint: true, onlyIfSuccessful: true
    }
  }
}
