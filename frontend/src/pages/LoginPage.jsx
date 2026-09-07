import { useEffect, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Alert, Button, Input, Spin, Typography } from 'antd'
import { LoginOutlined } from '@ant-design/icons'
import { APP_NAME } from '../appBrand'
import { fetchW3OAuth2Status } from '../api/services'
import BrandLogo from '../components/BrandLogo'
import { useRole } from '../contexts/roleState'

const { Text, Title } = Typography

const OAUTH2_ERROR_MESSAGES = {
  state_invalid: 'W3 登录状态已失效，请重新发起登录',
  provider_denied: 'W3 未授权本次登录',
  authorization_code_missing: 'W3 未返回授权码，请重新登录',
  token_exchange_failed: 'W3 登录凭据交换失败，请稍后重试',
  userinfo_failed: '无法获取 W3 用户信息，请稍后重试',
  employee_no_missing: 'W3 用户信息中缺少工号，请联系管理员',
  email_missing: 'W3 用户信息中缺少有效邮箱，请联系管理员',
  account_not_found: '该 W3 工号和邮箱尚未绑定同一系统账号，请联系管理员',
  account_inactive: '该系统账号已停用，请联系管理员',
}

function redirectBrowserToW3(url) {
  window.location.replace(url)
}

export default function LoginPage({ redirectToW3 = redirectBrowserToW3 }) {
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { completeW3OAuth2Login, loginWithDevToken } = useRole()
  const oauth2Error = searchParams.get('oauth2_error')
  const oauth2Success = searchParams.get('oauth2') === 'success'
  const [error, setError] = useState(
    oauth2Error
      ? OAUTH2_ERROR_MESSAGES[oauth2Error] || 'W3 登录失败，请重新登录'
      : '',
  )
  const [startUrl, setStartUrl] = useState(null)
  const [debugTokenLoginEnabled, setDebugTokenLoginEnabled] = useState(false)
  const [devToken, setDevToken] = useState('')
  const [devLoginLoading, setDevLoginLoading] = useState(false)
  const [statusAttempt, setStatusAttempt] = useState(0)
  const completingRef = useRef(false)

  useEffect(() => {
    let cancelled = false
    fetchW3OAuth2Status()
      .then(({ data }) => {
        if (cancelled) return
        const url = data?.ready ? data.start_url : null
        const allowDebugToken = Boolean(
          data?.debug_token_login_enabled && !data?.ready,
        )
        setStartUrl(url)
        setDebugTokenLoginEnabled(allowDebugToken)
        if (!oauth2Error && !oauth2Success) {
          if (url) {
            redirectToW3(url)
          } else if (allowDebugToken) {
            setError('')
          } else {
            setError('W3 登录尚未正确配置，请联系管理员')
          }
        }
      })
      .catch(() => {
        if (!cancelled && !oauth2Error && !oauth2Success) {
          setError('无法获取 W3 登录状态，请稍后重试')
        }
      })
    return () => {
      cancelled = true
    }
  }, [oauth2Error, oauth2Success, redirectToW3, statusAttempt])

  useEffect(() => {
    if (!oauth2Success || completingRef.current) return
    completingRef.current = true
    setError('')
    completeW3OAuth2Login()
      .then(() => navigate('/', { replace: true }))
      .catch((err) => {
        completingRef.current = false
        setError(err?.response?.data?.detail || 'W3 登录凭据领取失败，请重新登录')
    })
  }, [completeW3OAuth2Login, navigate, oauth2Success])

  const submitDevToken = async () => {
    const token = devToken.trim()
    if (!token) return
    setDevLoginLoading(true)
    setError('')
    try {
      await loginWithDevToken(token)
      navigate('/', { replace: true })
    } catch (err) {
      setError(err?.response?.data?.detail || '开发令牌无效或账号不可用')
    } finally {
      setDevLoginLoading(false)
    }
  }

  return (
    <div className="login-shell">
      <div className="login-redirect-card">
        <BrandLogo size={52} />
        <Title level={3}>{APP_NAME}</Title>
        {debugTokenLoginEnabled && !oauth2Success ? (
          <>
            <Alert
              type={error ? 'error' : 'info'}
              showIcon
              message={error || 'W3 登录尚未就绪，可使用本地开发令牌'}
            />
            <Input.Password
              autoComplete="off"
              placeholder="开发令牌"
              value={devToken}
              onChange={(event) => setDevToken(event.target.value)}
              onPressEnter={submitDevToken}
            />
            <Button
              block
              loading={devLoginLoading}
              disabled={!devToken.trim()}
              onClick={submitDevToken}
              size="large"
              type="primary"
            >
              使用开发令牌登录
            </Button>
            <Button
              type="link"
              onClick={() => {
                setDebugTokenLoginEnabled(false)
                setStatusAttempt((value) => value + 1)
              }}
            >
              重新检查 W3 登录
            </Button>
          </>
        ) : error ? (
          <>
            <Alert type="error" showIcon message={error} />
            <Button
              block
              icon={<LoginOutlined />}
              onClick={() => {
                if (startUrl) {
                  redirectToW3(startUrl)
                  return
                }
                setError('')
                setStatusAttempt((value) => value + 1)
              }}
              size="large"
              type="primary"
            >
              {startUrl ? '重新发起 W3 登录' : '重新检查 W3 登录'}
            </Button>
          </>
        ) : (
          <>
            <Spin size="large" />
            <Text type="secondary">
              {oauth2Success ? '正在完成 W3 登录…' : '正在跳转至 W3 登录…'}
            </Text>
          </>
        )}
      </div>
    </div>
  )
}
