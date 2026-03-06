import { describe, expect, it } from 'vitest'

import {
  TOKENS_PER_MILLION,
  calculateTokenPricePerMillion,
  calculateTokenUnitPrice,
  formatTokenPricePerMillion
} from '@/utils/usagePricing'

describe('usagePricing utils', () => {
  it('根据已存成本和 token 数反推历史单价', () => {
    expect(calculateTokenUnitPrice(0.003936, 1312)).toBeCloseTo(0.000003, 12)
    expect(calculateTokenPricePerMillion(0.003936, 1312)).toBeCloseTo(3, 10)
  })

  it('支持零成本模型，仍返回 0 单价', () => {
    expect(calculateTokenUnitPrice(0, 2048)).toBe(0)
    expect(calculateTokenPricePerMillion(0, 2048)).toBe(0)
    expect(formatTokenPricePerMillion(0, 2048)).toBe('$0.0000')
  })

  it('token 不合法时返回空值', () => {
    expect(calculateTokenUnitPrice(0.1, 0)).toBeNull()
    expect(calculateTokenUnitPrice(0.1, -1)).toBeNull()
    expect(calculateTokenPricePerMillion(0.1, undefined)).toBeNull()
    expect(formatTokenPricePerMillion(0.1, null)).toBe('-')
  })

  it('默认固定 4 位小数，适合财务比对', () => {
    expect(formatTokenPricePerMillion(25 * 10 / TOKENS_PER_MILLION, 10)).toBe('$25.0000')
    expect(formatTokenPricePerMillion(1.25 * 10 / TOKENS_PER_MILLION, 10)).toBe('$1.2500')
    expect(formatTokenPricePerMillion(0.125 * 10 / TOKENS_PER_MILLION, 10)).toBe('$0.1250')
  })

  it('导出时支持纯数字格式，便于 Excel 聚合', () => {
    expect(
      formatTokenPricePerMillion(3 * 1000 / TOKENS_PER_MILLION, 1000, {
        withCurrencySymbol: false
      })
    ).toBe('3.0000')
  })
})
