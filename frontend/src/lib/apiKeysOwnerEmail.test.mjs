import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import test from 'node:test'

const srcRoot = fileURLToPath(new URL('..', import.meta.url))

function read(relativePath) {
  return readFileSync(join(srcRoot, relativePath), 'utf8')
}

function readJSON(relativePath) {
  return JSON.parse(read(relativePath))
}

// 归属邮箱（owner_email）是会话存档按人查询的唯一归属维度：
// 进不了创建/编辑弹窗，管理员就只能改库，所以这里守住三件事：
//  1. 创建弹窗有字段，且提交 payload 里带 owner_email
//  2. 编辑抽屉有字段，且 PATCH payload 里带 owner_email（空串表示清空）
//  3. 列表行会显示邮箱，三种语言的文案键齐全
test('API key create dialog submits owner_email', () => {
  const source = read('pages/APIKeys.tsx')
  assert.match(source, /interface CreateKeyFormState \{[\s\S]*?ownerEmail: string/, 'CreateKeyFormState 缺少 ownerEmail')
  assert.match(source, /owner_email: createForm\.ownerEmail\.trim\(\)/, '创建 payload 缺少 owner_email')
  assert.match(source, /value=\{createForm\.ownerEmail\}/, '创建弹窗缺少归属邮箱输入框')
})

test('API key edit drawer updates owner_email (empty string clears it)', () => {
  const source = read('pages/APIKeys.tsx')
  assert.match(source, /interface EditKeyFormState \{[\s\S]*?ownerEmail: string/, 'EditKeyFormState 缺少 ownerEmail')
  assert.match(source, /owner_email: editForm\.ownerEmail\.trim\(\)/, '编辑 payload 缺少 owner_email')
  assert.match(source, /value=\{editForm\.ownerEmail\}/, '编辑抽屉缺少归属邮箱输入框')
  assert.match(source, /ownerEmail: keyRow\.owner_email \?\? ""/, '打开编辑时未回填 owner_email')
})

test('API key list renders owner_email when present', () => {
  const source = read('pages/APIKeys.tsx')
  const occurrences = (source.match(/\{keyRow\.owner_email \?/g) ?? []).length
  assert.ok(occurrences >= 2, `列表（卡片/表格）应显示归属邮箱，实际 ${occurrences} 处`)
})

test('owner_email labels exist in every locale', () => {
  for (const locale of ['zh', 'en']) {
    const strings = readJSON(`locales/${locale}.json`)
    for (const key of ['ownerEmailLabel', 'ownerEmailHelp', 'ownerEmailPlaceholder']) {
      assert.equal(typeof strings.apiKeys?.[key], 'string', `${locale}.json 缺少 apiKeys.${key}`)
    }
  }
})
