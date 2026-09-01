# 批量发布

复制 `inventory.example.ini` 为 `inventory.ini`，在 Ansible Vault 中定义 `vault_admin_secret`、`vault_database_password`（以及可选的 `vault_proxy_url`），然后先以单台 canary 验证：

```bash
ansible-playbook -i inventory.ini deploy.yml \
  -e codex2api_version=v2.8.2 \
  -e @vault.yml --limit customer-01
```

通过后再按批次并行发布：

```bash
ansible-playbook -i inventory.ini deploy.yml \
  -e codex2api_version=v2.8.2 \
  -e rollout_batch=10 -e @vault.yml
```

镜像引用可用 `codex2api_image` 覆盖为私有 Registry 或 digest。不要把 `vault.yml` 提交到仓库。
