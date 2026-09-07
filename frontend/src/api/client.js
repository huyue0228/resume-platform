import axios from 'axios'
import { message } from 'antd'

// Axios instance. baseURL is /api which Vite dev server proxies to
// http://localhost:8000 (see vite.config.js).
const client = axios.create({
  baseURL: '/api',
  timeout: 60000,
})

client.interceptors.request.use((config) => {
  const token = localStorage.getItem('srf_token')
  if (token) {
    config.headers.Authorization = `Token ${token}`
  }
  return config
})

client.interceptors.response.use(
  (response) => response,
  (error) => {
    if (error?.response?.status === 401) {
      localStorage.removeItem('srf_token')
      if (window.location.pathname !== '/login') {
        window.location.assign('/login')
      }
    }
    const detail =
      error?.response?.data?.detail ||
      error?.response?.data?.message ||
      error?.message ||
      '请求失败'
    message.error(String(detail))
    return Promise.reject(error)
  },
)

export default client
