import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccountTestModal from '../AccountTestModal.vue'

const { getAvailableModels } = vi.hoisted(() => ({
  getAvailableModels: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getAvailableModels
    }
  }
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn()
  })
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

describe('AccountTestModal', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
    getAvailableModels.mockReset()
    getAvailableModels.mockResolvedValue([
      { id: 'gpt-5', display_name: 'GPT-5' }
    ])
    localStorage.setItem('auth_token', 'token-123')
  })

  it('测试成功后会向父组件发出 success 事件', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      body: new ReadableStream({
        start(controller) {
          const encoder = new TextEncoder()
          controller.enqueue(encoder.encode('data: {"type":"test_start","model":"gpt-5"}\n\n'))
          controller.enqueue(encoder.encode('data: {"type":"test_complete","success":true}\n\n'))
          controller.close()
        }
      })
    }))

    const wrapper = mount(AccountTestModal, {
      props: {
        show: true,
        account: {
          id: 9,
          name: 'Test OpenAI Account',
          platform: 'sora',
          type: 'oauth',
          status: 'active'
        } as any
      },
      global: {
        stubs: {
          BaseDialog: {
            template: '<div><slot /><slot name="footer" /></div>'
          },
          Select: {
            props: ['modelValue', 'options'],
            emits: ['update:modelValue'],
            template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="opt in options" :key="opt.id" :value="opt.id">{{ opt.display_name }}</option></select>'
          },
          Icon: {
            template: '<span />'
          }
        }
      }
    })

    await flushPromises()

    const startButton = wrapper.findAll('button').find(button => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(fetch).toHaveBeenCalledWith('/api/v1/admin/accounts/9/test', expect.objectContaining({
      method: 'POST'
    }))
    expect(wrapper.emitted('success')).toEqual([[9]])
  })
})
