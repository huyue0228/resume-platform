function parseContentDispositionFilename(header) {
  if (!header) return ''
  const encodedMatch = header.match(/filename\*=UTF-8''([^;]+)/i)
  if (encodedMatch?.[1]) {
    try {
      return decodeURIComponent(encodedMatch[1])
    } catch {
      return encodedMatch[1]
    }
  }
  const plainMatch = header.match(/filename="?([^";]+)"?/i)
  return plainMatch?.[1] || ''
}

export function downloadBlobFromResponse(resp, fallbackName) {
  const filename =
    parseContentDispositionFilename(resp.headers?.['content-disposition']) ||
    fallbackName
  const contentType = resp.headers?.['content-type'] || 'application/octet-stream'
  const url = URL.createObjectURL(new Blob([resp.data], { type: contentType }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
  return filename
}
