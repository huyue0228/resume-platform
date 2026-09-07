export const ENABLED_KEY = 'ai_special_route_enabled'
export const THRESHOLD_KEY = 'ai_special_route_threshold'
export const SECONDARY_KEY = 'ai_special_route_secondary_department_id'
export const TERTIARY_KEY = 'ai_special_route_tertiary_department_id'

const VALUE_KEYS = [THRESHOLD_KEY, SECONDARY_KEY, TERTIARY_KEY]

export const DEFAULT_VALUES = {
  [ENABLED_KEY]: false,
  [THRESHOLD_KEY]: 0.9,
  [SECONDARY_KEY]: 0,
  [TERTIARY_KEY]: 0,
}

export async function saveAISpecialSettings({ persisted, drafts, update }) {
  const targetChanged = [SECONDARY_KEY, TERTIARY_KEY]
    .some((key) => drafts[key] !== persisted[key])
  let effectiveEnabled = Boolean(persisted[ENABLED_KEY])

  // 已启用时直接切换部门会被后端的层级关系校验拦截，因此先关闭，
  // 完成目标更新后再按用户最终选择恢复开关。
  if (effectiveEnabled && (!drafts[ENABLED_KEY] || targetChanged)) {
    await update(ENABLED_KEY, false)
    effectiveEnabled = false
  }

  for (const key of VALUE_KEYS) {
    if (drafts[key] !== persisted[key]) {
      await update(key, drafts[key])
    }
  }

  if (Boolean(drafts[ENABLED_KEY]) !== effectiveEnabled) {
    await update(ENABLED_KEY, Boolean(drafts[ENABLED_KEY]))
  }
}
