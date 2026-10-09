import assert from 'node:assert/strict'
import test from 'node:test'

import { isAnonymousBidCheckUploadUrl, isSkillBundleUploadUrl } from './uploadLimit.ts'

test('skill catalog and sandbox skill uploads use the skill-bundle 413 path', () => {
  assert.equal(isSkillBundleUploadUrl('/api/v1/skills/catalog'), true)
  assert.equal(isSkillBundleUploadUrl('/api/v1/skills/catalog/'), true)
  assert.equal(isSkillBundleUploadUrl('/api/v1/skills/catalog?foo=1'), true)
  assert.equal(isSkillBundleUploadUrl('/api/v1/sandbox-configs/cfg-a/skills'), true)
  assert.equal(isSkillBundleUploadUrl('https://host/api/v1/sandbox-configs/cfg-a/skills'), true)
  assert.equal(isSkillBundleUploadUrl('skills/catalog'), true)
  assert.equal(isSkillBundleUploadUrl('sandbox-configs/cfg-a/skills'), true)
})

test('skill JSON subpaths stay on the knowledge-size 413 path', () => {
  assert.equal(isSkillBundleUploadUrl('/api/v1/skills/catalog/cat-1/install'), false)
  assert.equal(isSkillBundleUploadUrl('/api/v1/skills/catalog/cat-1'), false)
  assert.equal(isSkillBundleUploadUrl('/api/v1/sandbox-configs/cfg-a/skills/sk-1'), false)
  assert.equal(isSkillBundleUploadUrl('/api/v1/sandbox-configs/cfg-a/skills/sk-1/reinstall'), false)
})

test('knowledge and other API uploads stay on the knowledge-size 413 path', () => {
  assert.equal(isSkillBundleUploadUrl(undefined), false)
  assert.equal(isSkillBundleUploadUrl('/api/v1/knowledge'), false)
  assert.equal(isSkillBundleUploadUrl('/api/v1/knowledge-bases/kb-1/knowledge'), false)
  assert.equal(isSkillBundleUploadUrl('/api/v1/models/debug'), false)
})

test('anonymous bid checks use their own size error without changing other upload limits', () => {
  for (const url of ['/api/v1/anonymous-bid-check', '/api/v1/anonymous-bid-check/', '/api/v1/anonymous-bid-check?scope=technical', 'https://host/api/v1/anonymous-bid-check']) {
    assert.equal(isAnonymousBidCheckUploadUrl(url), true)
    assert.equal(isSkillBundleUploadUrl(url), false)
  }
  for (const url of [undefined, '/api/v1/knowledge', '/api/v1/skills/catalog', '/api/v1/anonymous-bid-check/report']) {
    assert.equal(isAnonymousBidCheckUploadUrl(url), false)
  }
})
