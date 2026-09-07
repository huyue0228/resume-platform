import { APP_NAME } from '../appBrand'

export default function BrandLogo({ size = 32, className }) {
  return (
    <img
      src="/favicon.svg"
      alt={`${APP_NAME}标志`}
      className={className}
      width={size}
      height={size}
    />
  )
}
