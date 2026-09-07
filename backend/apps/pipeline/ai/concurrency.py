"""跨 Celery worker 的 AI 模型自适应并发控制。

并发额度按模型连接隔离并保存在 Redis。单次请求使用带过期时间的租约，
worker 异常退出后不会永久占用额度。429 会降低额度并进入冷却期；正常
请求按 2 -> 4 -> 8 的方式慢启动，始终受 ``ai_concurrency`` 上限约束。
"""
from __future__ import annotations

import hashlib
import random
import time
import uuid
from dataclasses import dataclass

from django.conf import settings
from django.db.models import F
from django.utils import timezone


class AIConcurrencyError(RuntimeError):
    pass


_ACQUIRE_SCRIPT = """
local now = tonumber(ARGV[1])
local ceiling = tonumber(ARGV[2])
local lease_id = ARGV[3]
local expires_at = tonumber(ARGV[4])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now)
local current = tonumber(redis.call('HGET', KEYS[2], 'limit') or '0')
if current < 1 then current = math.min(2, ceiling) end
if current > ceiling then current = ceiling end
local blocked_until = tonumber(redis.call('HGET', KEYS[2], 'blocked_until') or '0')
local in_flight = tonumber(redis.call('ZCARD', KEYS[1]))
redis.call('HSET', KEYS[2], 'limit', current, 'ceiling', ceiling)
redis.call('EXPIRE', KEYS[1], 3600)
redis.call('EXPIRE', KEYS[2], 3600)
if blocked_until > now then
  return {0, current, in_flight, math.ceil((blocked_until - now) * 1000)}
end
if in_flight < current then
  redis.call('ZADD', KEYS[1], expires_at, lease_id)
  redis.call('EXPIRE', KEYS[1], 3600)
  return {1, current, in_flight + 1, 0}
end
return {0, current, in_flight, 200}
"""

_FEEDBACK_SCRIPT = """
local now = tonumber(ARGV[1])
local ceiling = tonumber(ARGV[2])
local outcome = ARGV[3]
local retry_after = tonumber(ARGV[4])
local backoff = tonumber(ARGV[5])
local current = tonumber(redis.call('HGET', KEYS[2], 'limit') or '0')
if current < 1 then current = math.min(2, ceiling) end
if current > ceiling then current = ceiling end
redis.call('ZREM', KEYS[1], ARGV[6])
if outcome == 'success' then
  local blocked_until = tonumber(redis.call('HGET', KEYS[2], 'blocked_until') or '0')
  local growth_blocked_until = tonumber(redis.call('HGET', KEYS[2], 'growth_blocked_until') or '0')
  local successes = tonumber(redis.call('HINCRBY', KEYS[2], 'successes', 1))
  if now >= blocked_until and now >= growth_blocked_until and successes >= current and current < ceiling then
    current = math.min(ceiling, current * 2)
    redis.call('HSET', KEYS[2], 'successes', 0)
  end
elseif outcome == 'rate_limit' then
  local last_reduce = tonumber(redis.call('HGET', KEYS[2], 'last_reduce') or '0')
  if now - last_reduce >= 15 then
    current = math.max(1, math.floor(current * 0.8))
    redis.call('HSET', KEYS[2], 'last_reduce', now)
  end
  local pause = math.max(retry_after, backoff, 1)
  local blocked_until = tonumber(redis.call('HGET', KEYS[2], 'blocked_until') or '0')
  if now + pause > blocked_until then
    redis.call('HSET', KEYS[2], 'blocked_until', now + pause)
  end
  redis.call('HSET', KEYS[2], 'successes', 0)
elseif outcome == 'transient' then
  local growth_blocked_until = tonumber(redis.call('HGET', KEYS[2], 'growth_blocked_until') or '0')
  local pause_until = now + math.max(backoff, 1)
  if pause_until > growth_blocked_until then
    redis.call('HSET', KEYS[2], 'growth_blocked_until', pause_until)
  end
  redis.call('HSET', KEYS[2], 'successes', 0)
end
redis.call('HSET', KEYS[2], 'limit', current, 'ceiling', ceiling)
redis.call('EXPIRE', KEYS[1], 3600)
redis.call('EXPIRE', KEYS[2], 3600)
return {current, tonumber(redis.call('ZCARD', KEYS[1]))}
"""


def _redis_client():
    try:
        import redis

        return redis.Redis.from_url(
            settings.CELERY_BROKER_URL,
            socket_connect_timeout=3,
            socket_timeout=3,
            decode_responses=True,
        )
    except Exception as exc:  # pragma: no cover - 缺依赖只会发生在部署配置错误时
        raise AIConcurrencyError("AI 并发控制器初始化失败") from exc


def _resource_key(model_config):
    secret_fingerprint = hashlib.sha256(
        model_config.api_key.encode("utf-8")
    ).hexdigest()
    payload = "|".join(
        [
            model_config.api_style,
            model_config.base_url.rstrip("/"),
            model_config.model_name,
            secret_fingerprint,
        ]
    )
    digest = hashlib.sha256(payload.encode("utf-8")).hexdigest()
    return f"srf:ai-limit:{{{digest}}}:leases", f"srf:ai-limit:{{{digest}}}:state"


def _record_effective_limit(run_id, current):
    if not run_id:
        return
    from apps.core.models import ProcessingRun

    ProcessingRun.objects.filter(pk=run_id).update(
        ai_effective_concurrency=current,
        last_heartbeat_at=timezone.now(),
    )


def record_retry(run_id):
    if not run_id:
        return
    from apps.core.models import ProcessingRun

    ProcessingRun.objects.filter(pk=run_id).update(
        ai_retry_count=F("ai_retry_count") + 1
    )


def record_rate_limit(run_id):
    if not run_id:
        return
    from apps.core.models import ProcessingRun

    ProcessingRun.objects.filter(pk=run_id).update(
        ai_rate_limit_count=F("ai_rate_limit_count") + 1
    )


@dataclass
class ModelSlot:
    client: object | None
    leases_key: str
    state_key: str
    lease_id: str
    ceiling: int
    backoff: int
    run_id: int | None
    local: bool = False
    released: bool = False

    def release(self, outcome="neutral", *, retry_after=0):
        if self.released:
            return
        self.released = True
        if self.local:
            return
        try:
            current, _in_flight = self.client.eval(
                _FEEDBACK_SCRIPT,
                2,
                self.leases_key,
                self.state_key,
                time.time(),
                self.ceiling,
                outcome,
                max(0, float(retry_after or 0)),
                max(0, self.backoff),
                self.lease_id,
            )
            _record_effective_limit(self.run_id, int(current))
        except Exception as exc:
            raise AIConcurrencyError("AI 并发额度释放失败") from exc


def acquire_slot(model_config, runtime_config, *, run_id=None, cancelled=None):
    """等待并取得一次模型 HTTP 请求额度。"""
    ceiling = max(1, int(runtime_config.concurrency))
    if getattr(settings, "CELERY_TASK_ALWAYS_EAGER", False):
        _record_effective_limit(run_id, 1)
        return ModelSlot(
            client=None,
            leases_key="",
            state_key="",
            lease_id=str(uuid.uuid4()),
            ceiling=ceiling,
            backoff=runtime_config.retry_backoff_seconds,
            run_id=run_id,
            local=True,
        )

    client = _redis_client()
    leases_key, state_key = _resource_key(model_config)
    lease_id = str(uuid.uuid4())
    lease_seconds = runtime_config.timeout_seconds + 60
    while True:
        if cancelled and cancelled():
            raise AIConcurrencyError("AI 调用已取消")
        now = time.time()
        try:
            acquired, current, _in_flight, wait_ms = client.eval(
                _ACQUIRE_SCRIPT,
                2,
                leases_key,
                state_key,
                now,
                ceiling,
                lease_id,
                now + lease_seconds,
            )
        except Exception as exc:
            raise AIConcurrencyError("AI 并发控制器不可用") from exc
        _record_effective_limit(run_id, int(current))
        if int(acquired):
            return ModelSlot(
                client=client,
                leases_key=leases_key,
                state_key=state_key,
                lease_id=lease_id,
                ceiling=ceiling,
                backoff=runtime_config.retry_backoff_seconds,
                run_id=run_id,
            )
        delay = min(1.0, max(0.05, int(wait_ms) / 1000))
        time.sleep(delay + random.uniform(0, min(0.15, delay / 2)))


def retry_delay(runtime_config, retry_index, *, retry_after=0):
    if retry_after:
        return max(0, float(retry_after))
    base = max(0, runtime_config.retry_backoff_seconds)
    maximum = max(base, min(300, base * (2 ** max(0, retry_index))))
    return random.uniform(0, maximum) if maximum else 0
