import { useEffect, useState } from 'react'
import { Alert, Button, Empty, Skeleton, Tabs, Tag } from 'antd'
import { ArrowRightOutlined, CheckCircleOutlined, ReloadOutlined } from '@ant-design/icons'
import { fetchAgentDecision } from '../api/services'
import './CandidateAnalysis.css'

const DIMENSIONS = [
  ['major_match', '专业方向', 30],
  ['skills_match', '技能匹配', 20],
  ['experience_evidence', '项目与实习', 25],
  ['job_requirement', '职责覆盖', 15],
  ['resume_quality', '材料完整度', 10],
]
const CLAIM_LABELS = {
  education: '教育背景', project: '项目经历', internship: '实习经历', skill: '专业技能',
  certificate: '证书与资质', major_direction: '专业方向', agent_experience: '智能体专项经历', risk: '需要核实',
}
const OUTCOMES = {
  dispatch: ['可进入下发', 'success'], review: ['需要人工复核', 'warning'], archive: ['暂不进入分配', 'default'],
}
const readableRisk = (risk) => ({ profile_incomplete: '简历信息不足，需人工核实', ocr_fallback: '扫描材料经文字识别处理' }[risk] || risk)
const scoreText = (score) => score == null || !Number.isFinite(Number(score)) ? '—' : Math.round(Number(score) * 100)

function Evidence({ items = [] }) {
  if (!items.length) return <p className="candidate-analysis-muted">该记录没有保留可定位的原文证据。</p>
  return (
    <div className="candidate-analysis-evidence">
      {items.map((item, index) => {
        const evidence = typeof item === 'string' ? { quote: item } : item
        return (
          <figure key={`${evidence.start_line || index}-${evidence.quote}`}>
            <blockquote>{evidence.quote}</blockquote>
            <figcaption>简历原文{evidence.page ? ` · 第 ${evidence.page} 页 · 第 ${evidence.start_line}–${evidence.end_line} 行` : ''}</figcaption>
          </figure>
        )
      })}
    </div>
  )
}

function Dimensions({ values = {} }) {
  return (
    <div className="candidate-analysis-dimensions" aria-label="匹配维度">
      {DIMENSIONS.map(([key, label, weight]) => {
        const value = values[key]
        return (
          <div className="candidate-analysis-dimension" key={key}>
            <span>{label}<small>权重 {weight}%</small></span>
            <meter min="0" max="1" value={Number(value) || 0} aria-label={`${label} ${scoreText(value)} 分`} />
            <strong>{scoreText(value)}</strong>
          </div>
        )
      })}
      <p className="candidate-analysis-muted">综合匹配分按固定权重计算，不代表录用概率。</p>
    </div>
  )
}

function Risks({ items = [] }) {
  return items.length ? (
    <section className="candidate-analysis-risks" aria-label="需要关注">
      <h4>需要关注</h4>
      <ul>{[...new Set(items)].map((risk) => <li key={risk}>{readableRisk(risk)}</li>)}</ul>
    </section>
  ) : null
}

function MatchWorkspace({ matches }) {
  const [selectedRef, setSelectedRef] = useState(null)
  const selected = matches.find((match) => match.job_ref === selectedRef) || matches.find((match) => match.is_selected) || matches[0]
  if (!selected) return <Empty description="这次记录没有完整岗位排名" />
  return (
    <div className="candidate-analysis-workspace">
      <aside className="candidate-analysis-joblist" aria-label="合规岗位排名">
        <div className="candidate-analysis-section-label">合规岗位 <span>{matches.length}</span></div>
        {matches.map((match) => (
          <button
            type="button"
            key={match.job_ref}
            className={`candidate-analysis-job ${selected.job_ref === match.job_ref ? 'is-selected' : ''}`}
            aria-pressed={selected.job_ref === match.job_ref}
            onClick={() => setSelectedRef(match.job_ref)}
          >
            <span className="candidate-analysis-rank">{String(match.rank).padStart(2, '0')}</span>
            <span className="candidate-analysis-jobname"><strong>{match.job_title || '岗位名称不可用'}</strong><small>{match.department_name || '部门信息未保留'}</small>{match.is_selected && <em>本次选定岗位</em>}</span>
            <span className="candidate-analysis-jobscore">{scoreText(match.score)}</span>
          </button>
        ))}
        <p className="candidate-analysis-muted">按匹配分排序。最终分配还会检查实时名额，可能与第一名不同。</p>
      </aside>
      <section className="candidate-analysis-match" aria-label="所选岗位分析">
        <header className="candidate-analysis-match-heading">
          <div><span className="candidate-analysis-section-label">岗位匹配分析</span><h3>{selected.job_title || '岗位名称不可用'}</h3><p>{selected.department_name}</p></div>
          <div className="candidate-analysis-score"><strong>{scoreText(selected.score)}</strong><span>/ 100 匹配分</span></div>
        </header>
        <p className="candidate-analysis-reason">{selected.reason}</p>
        <Dimensions values={selected.dimensions} />
        <h4>支撑这项判断的证据</h4>
        <Evidence items={selected.evidence} />
        <Risks items={selected.risks} />
      </section>
    </div>
  )
}

function Profile({ profile }) {
  if (!profile?.claims?.length) return <Empty description="该历史记录没有结构化候选人画像" />
  return (
    <div className="candidate-analysis-profile">
      {Object.entries(CLAIM_LABELS).map(([kind, label]) => {
        const claims = profile.claims.filter((claim) => claim.kind === kind)
        return claims.length ? (
          <section key={kind} className="candidate-analysis-claim-group">
            <h3>{label}</h3>
            <div>{claims.map((claim, index) => (
              <article key={`${kind}-${index}`}>
                <p>{claim.summary}</p>
                <details><summary>查看原文依据 <span>{claim.evidence?.length || 0} 处</span></summary><Evidence items={claim.evidence} /></details>
              </article>
            ))}</div>
          </section>
        ) : null
      })}
      <Risks items={profile.risks} />
    </div>
  )
}

function Diagnostics({ decision }) {
  const result = decision.kernel_result || {}
  const trace = result.safe_trace || decision.safe_trace || {}
  const tools = trace.tool_calls || []
  return (
    <details className="candidate-analysis-diagnostics">
      <summary>运行记录与版本 <span>供问题排查使用</span></summary>
      <dl>
        <div><dt>分析模型</dt><dd>{decision.model_name || '未记录'}</dd></div>
        <div><dt>内核版本</dt><dd>{decision.kernel_build || '历史版本'}</dd></div>
        <div><dt>模型轮次</dt><dd>{trace.turns ?? '—'}</dd></div>
        <div><dt>Token 用量</dt><dd>{trace.input_tokens == null ? '—' : (Number(trace.input_tokens) + Number(trace.output_tokens || 0)).toLocaleString('zh-CN')}</dd></div>
      </dl>
      {tools.length > 0 && <div className="candidate-analysis-trace"><table><caption>阶段与工具调用</caption><thead><tr><th>阶段 / 工具</th><th>状态</th><th>耗时</th></tr></thead><tbody>{tools.map((tool, index) => <tr key={`${tool.name}-${index}`}><td>{tool.name}</td><td>{['ok', 'success', 'ready'].includes(tool.status) ? '完成' : ['error', 'rejected'].includes(tool.status) ? '未完成' : tool.status}</td><td>{tool.duration_ms} ms</td></tr>)}</tbody></table></div>}
    </details>
  )
}

function LegacyAnalysis({ decision }) {
  return (
    <div className="candidate-analysis-legacy">
      <p className="candidate-analysis-muted">历史单岗位分析 · 未记录完整岗位排名</p>
      <h3>{decision.evaluated_job_name || decision.recommended_job_name || '岗位分析'}</h3>
      <p>{decision.recommended_department_name}</p>
      <p className="candidate-analysis-reason">{decision.reason || decision.summary || '这次未形成有效分析结果。'}</p>
      <Dimensions values={decision.score_breakdown} />
      <h4>简历证据</h4><Evidence items={decision.evidence} /><Risks items={decision.risks} />
    </div>
  )
}

export default function CandidateAnalysis({ decision, onRetry, retrying = false }) {
  const [loaded, setLoaded] = useState(null)
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const [reload, setReload] = useState(0)
  useEffect(() => {
    let cancelled = false
    setLoaded(null)
    setLoadError(false)
    setLoading(false)
    if (!decision?.id || Array.isArray(decision.kernel_result?.matches)) return undefined
    setLoading(true)
    fetchAgentDecision(decision.id).then(({ data }) => {
      if (!cancelled) setLoaded(data)
    }).catch(() => { if (!cancelled) setLoadError(true) }).finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [decision, reload])
  if (!decision) return null
  const current = loaded || decision
  const result = current.kernel_result || {}
  const hasMatches = Array.isArray(result.matches) && result.matches.length > 0
  const [outcome, color] = current.error_code ? ['分析未完成', 'error'] : OUTCOMES[current.recommendation] || ['等待处理', 'default']
  return (
    <div className="candidate-analysis">
      <header className="candidate-analysis-overview">
        <div><div className="candidate-analysis-eyebrow">候选人分析</div><h2>{current.candidate_name || '简历'}<span>{current.position_name || '当前志愿'}</span></h2><p>从简历证据到岗位比较，每项判断都可追溯。</p></div>
        <Tag color={color}>{outcome}</Tag>
      </header>
      {current.error_code && <Alert showIcon type="warning" message={current.error_message || '这次分析未完成'} description="可以重新分析，也可以返回候选人详情进行人工分配。" />}
      {loadError && <Alert showIcon type="warning" message="完整分析加载失败，当前展示已有记录" action={<Button size="small" onClick={() => setReload((value) => value + 1)}>重新加载</Button>} />}
      {result.manifest?.reused_from_task_id && <p className="candidate-analysis-reused"><CheckCircleOutlined /> 已复用内容未变化的分析，本次仅重新检查分配条件。</p>}
      {loading && current.kernel_result?.match_count ? <Skeleton active paragraph={{ rows: 8 }} /> : hasMatches ? (
        <Tabs items={[
          { key: 'matches', label: `岗位比较 · ${result.matches.length}`, children: <MatchWorkspace matches={result.matches} /> },
          { key: 'profile', label: '候选人画像', children: <Profile profile={result.profile} /> },
        ]} />
      ) : <LegacyAnalysis decision={current} />}
      <Diagnostics decision={current} />
      {onRetry && (current.error_code || current.recommendation === 'archive') && <footer className="candidate-analysis-footer"><span>补充材料或修正岗位配置后，可重新分析。</span><Button icon={<ReloadOutlined />} loading={retrying} onClick={() => onRetry(current)}>重新分析 <ArrowRightOutlined /></Button></footer>}
    </div>
  )
}
