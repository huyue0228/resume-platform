import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import CandidateAnalysis from './CandidateAnalysis'

vi.mock('../api/services', () => ({ fetchAgentDecision: vi.fn().mockRejectedValue(new Error('offline')) }))

const evidence = (quote) => [{ quote, page: 2, start_line: 8, end_line: 10 }]
const dimensions = { major_match: .9, skills_match: .8, experience_evidence: .85, job_requirement: .7, resume_quality: .9 }
const decision = {
  candidate_name: '林同学', position_name: '软件研发', recommendation: 'dispatch', model_name: '测试模型',
  kernel_result: {
    matches: [
      { job_ref: 'opaque-a', rank: 1, job_title: '后端开发', department_name: '基础平台部', score: .9, dimensions, reason: '服务开发经历支持平台岗位', evidence: evidence('负责服务拆分与查询性能优化'), risks: [] },
      { job_ref: 'opaque-b', rank: 2, job_title: '智能应用开发', department_name: '应用研发部', score: .8, dimensions, reason: '检索项目支持应用开发', evidence: evidence('实现检索增强的知识问答系统'), risks: ['需核实项目中的个人职责'], is_selected: true },
    ],
    profile: { claims: [{ kind: 'project', summary: '具备知识检索项目经历', evidence: evidence('实现检索增强的知识问答系统') }], risks: [] },
    manifest: { terminal_state: 'DONE' },
  },
}

describe('CandidateAnalysis', () => {
  it('explains an early budget stop and separates model time from local tools', () => {
    render(<CandidateAnalysis decision={{ ...decision, kernel_result: { ...decision.kernel_result, safe_trace: {
      turns: 15, input_tokens: 90000, output_tokens: 6338, tool_call_count: 18,
      budget: { max_turns: 32, max_tokens: 120000, max_context_tokens: 32768, max_tool_calls: 256, remaining_tokens: 23662, next_input_tokens: 24000, reserved_tokens: 0, stop_reason: 'next_request', usage_source: 'mixed', compactions: 2, repeated_calls: 1 },
      rounds: [{ turn: 15, phase: 'finalize', estimated_input_tokens: 8000, input_tokens: 7900, output_tokens: 1000, output_limit: 4096, reserved_tokens: 0, model_duration_ms: 2300, usage_source: 'reported', progress: false }],
      tool_calls: [{ name: 'candidate_profile.submit', status: 'error', duration_ms: 0, error_code: 'evidence_mismatch', error_field: 'claims[0].evidence[0].quote', repeated: true }],
    } } }} />)
    expect(screen.getByText('15 / 32')).toBeTruthy()
    expect(screen.getByText('96,338')).toBeTruthy()
    expect(screen.getByText('23,662')).toBeTruthy()
    expect(screen.getByText('剩余额度不足以完成下一轮')).toBeTruthy()
    expect(screen.getByText('包含估算用量')).toBeTruthy()
    expect(screen.getByText('2,300 ms')).toBeTruthy()
    expect(screen.getByText(/耗时不含模型请求/)).toBeTruthy()
    expect(screen.getByText(/claims\[0\]\.evidence\[0\]\.quote/)).toBeTruthy()
  })
  it('keeps a closed application readable without offering to overwrite it', () => {
    render(<CandidateAnalysis decision={{ ...decision, recommendation: 'archive', can_retry: false }} onRetry={vi.fn()} />)
    expect(screen.getByText('当前志愿不通过')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /重新分析/ })).toBeNull()
  })

  it('shows only the current application assessment and separates admission from allocation', () => {
    const current = { ...decision, pool_membership: { status: 'pending_allocation', assessment: { pool: { name: '机械工程师池' }, tag_catalog: [{ code: 'cad', name: '三维设计' }] } }, kernel_result: { ...decision.kernel_result, protocol_version: 'resume-analysis/v4', matches: [{ ...decision.kernel_result.matches[0], job_title: '机械工程师投递标准' }], profile: { tags: [{ code: 'cad', status: 'supported', evidence: evidence('完成三维机构设计与验证') }] } } }
    render(<CandidateAnalysis decision={current} />)
    expect(screen.getByRole('tab', { name: '当前投递契合度' })).toBeTruthy()
    expect(screen.getByText('入池待分配')).toBeTruthy()
    expect(screen.getByText('完成三维机构设计与验证')).toBeTruthy()
    expect(screen.queryByRole('complementary', { name: '合规岗位排名' })).toBeNull()
    expect(screen.queryByText('智能应用开发')).toBeNull()
  })
  it('selects the assigned job and lets HR compare another rank with its own evidence', async () => {
    render(<CandidateAnalysis decision={decision} />)
    const panel = screen.getByRole('region', { name: '所选岗位分析' })
    expect(within(panel).getByRole('heading', { name: '智能应用开发' })).toBeTruthy()
    expect(within(panel).getByText('需核实项目中的个人职责')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: /后端开发/ }))
    expect(within(panel).getByText('负责服务拆分与查询性能优化')).toBeTruthy()
    expect(within(panel).queryByText('需核实项目中的个人职责')).toBeNull()
    expect(screen.queryByText('opaque-a')).toBeNull()
  })

  it('shows profile evidence separately from job matching and keeps diagnostics collapsed', async () => {
    render(<CandidateAnalysis decision={decision} />)
    await userEvent.click(screen.getByRole('tab', { name: '候选人画像' }))
    expect(screen.getByText('具备知识检索项目经历')).toBeTruthy()
    const summary = screen.getByText('查看原文依据')
    expect(summary.closest('details').open).toBe(false)
    await userEvent.click(summary)
    expect(summary.closest('details').open).toBe(true)
    expect(screen.getByText('运行记录与版本').closest('details').open).toBe(false)
  })

  it('retains a readable history view without inventing a multi-job ranking', () => {
    render(<CandidateAnalysis decision={{ recommendation: 'review', evaluated_job_name: '历史岗位', reason: '需要核实经历', evidence: ['原有简历证据'], score_breakdown: dimensions }} />)
    expect(screen.getByText('历史复核记录')).toBeTruthy()
    expect(screen.getByText('历史单岗位分析 · 未记录完整岗位排名')).toBeTruthy()
    expect(screen.getByText('原有简历证据')).toBeTruthy()
    expect(screen.queryByRole('complementary', { name: '合规岗位排名' })).toBeNull()
  })
})
