import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Result, Spin } from 'antd'
import { APP_NAME } from '../appBrand'
import { fetchW3OAuth2Status } from '../api/services'
import BrandLogo from '../components/BrandLogo'
import { useRole } from '../contexts/roleState'

const ERRORS = {
  state_invalid: '认证状态校验未通过。',
  provider_denied: 'W3 未授权本次访问。',
  authorization_code_missing: '未收到 W3 授权信息。',
  token_exchange_failed: '无法完成 W3 认证凭据交换。',
  userinfo_failed: '无法获取 W3 用户信息。',
  employee_no_missing: 'W3 用户信息缺少工号。',
  email_missing: 'W3 用户信息缺少有效邮箱。',
  account_not_found: '当前 W3 账号尚未绑定系统账号。',
  account_inactive: '当前系统账号已停用。',
}
const redirectBrowserToW3 = (url) => window.location.replace(url)

// /login remains the W3 callback route; it contains no login form or retry action.
export default function AuthenticationPage({ redirectToW3 = redirectBrowserToW3 }) {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const { completeW3OAuth2Login } = useRole()
  const oauthError = params.get('oauth2_error')
  const success = params.get('oauth2') === 'success'
  const [error, setError] = useState(oauthError ? ERRORS[oauthError] || 'W3 认证未完成。' : '')
  const completionRef = useRef(null)

  useEffect(() => {
    if (oauthError) return undefined
    let active = true
    if (success) {
      // Preserve the one-time handoff promise across StrictMode effect replays.
      completionRef.current ||= completeW3OAuth2Login()
      completionRef.current.then(() => {
        if (active) navigate('/', { replace: true })
      }).catch(() => { if (active) setError('无法完成 W3 认证。') })
    } else {
      fetchW3OAuth2Status().then(({ data }) => {
        if (!active) return
        if (data?.ready && data.start_url) redirectToW3(data.start_url)
        else setError('W3 认证服务尚未就绪。')
      }).catch(() => { if (active) setError('无法连接 W3 认证服务。') })
    }
    return () => { active = false }
  }, [oauthError, success, completeW3OAuth2Login, navigate, redirectToW3])

  return <main className="authentication-shell">
    <div className="authentication-brand"><BrandLogo size={28} /><span>{APP_NAME}</span></div>
    {error ? <section role="alert" className="authentication-message">
      <Result status="warning" title="暂时无法访问" subTitle={<>{error}<br />请联系管理员检查 W3 认证配置及账号授权。</>} />
    </section> : <div className="authentication-pending" role="status"><Spin size="large" /><p>{success ? '正在完成企业认证…' : '正在连接企业认证…'}</p></div>}
  </main>
}
