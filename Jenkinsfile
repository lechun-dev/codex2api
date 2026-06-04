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
