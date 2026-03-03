import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import BulkEditAccountModal from '../BulkEditAccountModal.vue'

const { bulkUpdateMock } = vi.hoisted(() => ({
  bulkUpdateMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      bulkUpdate: bulkUpdateMock
    }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function mountModal(
  scope: { scopePlatform: string; scopeType: string } = {
    scopePlatform: 'gemini',
    scopeType: 'apikey'
  },
  selectStub: boolean | Record<string, unknown> = true
) {
  return mount(BulkEditAccountModal, {
    props: {
      show: true,
      accountIds: [1, 2],
      scopePlatform: scope.scopePlatform,
      scopeType: scope.scopeType,
      proxies: [],
      groups: []
    } as any,
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Select: selectStub,
        ProxySelector: true,
        GroupSelector: true,
        Icon: true
      }
    }
  })
}

describe('BulkEditAccountModal', () => {
  beforeEach(() => {
    bulkUpdateMock.mockReset()
  })

  it('OpenAI OAuth 选择 WS mode 后会写入 bulkUpdate payload', async () => {
    bulkUpdateMock.mockResolvedValue({ success: 2, failed: 0 })
    const wrapper = mountModal(
      { scopePlatform: 'openai', scopeType: 'oauth' },
      {
        props: ['options', 'modelValue'],
        template: `
          <select
            data-testid="select-stub"
            :value="modelValue"
            @change="$emit('update:modelValue', $event.target.value)"
          >
            <option v-for="option in options" :key="option.value" :value="option.value">
              {{ option.value }}
            </option>
          </select>
        `
      }
    )

    await wrapper.get('#bulk-edit-openai-ws-mode-enabled').setValue(true)
    const selects = wrapper.findAll('[data-testid="select-stub"]')
    const wsModeSelect = selects.find(
      (select) => select.text().includes('off') && select.text().includes('ctx_pool') && select.text().includes('passthrough')
    )
    expect(wsModeSelect).toBeTruthy()
    await wsModeSelect!.setValue('passthrough')

    await wrapper.get('form#bulk-edit-account-form').trigger('submit.prevent')

    expect(bulkUpdateMock).toHaveBeenCalledTimes(1)
    expect(bulkUpdateMock).toHaveBeenCalledWith(
      [1, 2],
      expect.objectContaining({
        extra: expect.objectContaining({
          openai_oauth_responses_websockets_v2_mode: 'passthrough'
        })
      })
    )
  })

  it('Gemini 范围白名单包含图片模型并过滤 GPT 模型', () => {
    const wrapper = mountModal()

    expect(wrapper.text()).toContain('Gemini 3.1 Flash Image')
    expect(wrapper.text()).toContain('Gemini 3 Pro Image (Legacy)')
    expect(wrapper.text()).not.toContain('GPT-5.3 Codex')
  })

  it('Gemini 范围映射预设包含图片映射并过滤 OpenAI 预设', async () => {
    const wrapper = mountModal()

    const mappingTab = wrapper.findAll('button').find((btn) => btn.text().includes('admin.accounts.modelMapping'))
    expect(mappingTab).toBeTruthy()
    await mappingTab!.trigger('click')

    expect(wrapper.text()).toContain('Gemini 3.1 Image')
    expect(wrapper.text()).toContain('G3 Image→3.1')
    expect(wrapper.text()).not.toContain('GPT-5.3 Codex')
  })

  it('OpenAI OAuth 范围 WS mode 选项仅保留 off、ctx_pool 与 passthrough', () => {
    const wrapper = mountModal(
      { scopePlatform: 'openai', scopeType: 'oauth' },
      {
        props: ['options'],
        template:
          '<div><span v-for="option in options" :key="option.value">{{ option.value }}</span></div>'
      }
    )

    expect(wrapper.text()).toContain('off')
    expect(wrapper.text()).toContain('ctx_pool')
    expect(wrapper.text()).toContain('passthrough')
    expect(wrapper.text()).not.toContain('shared')
    expect(wrapper.text()).not.toContain('dedicated')
  })
})
