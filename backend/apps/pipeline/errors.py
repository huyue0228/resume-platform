"""平台与引擎适配共用的受控错误，不依赖模型 SDK。"""

class AIServiceError(Exception):
    def __init__(self, code, message, *, profile=None, safe_trace=None):
        message = str(message).replace("\x00", "")
        super().__init__(message)
        self.code = code
        self.message = message
        self.profile = profile
        self.safe_trace = safe_trace or {}
