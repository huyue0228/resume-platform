"""独立验证已固定的协议分发物；消费者 CI 不需要协议仓源码。"""
import hashlib
import json
from pathlib import Path
from .models import AnalysisRequestV1, AnalysisResponseV1, KernelCapabilitiesV1


def verify():
    root=Path(__file__).resolve().parent
    bundle=root/"bundle"
    manifest=json.loads((bundle/"manifest.json").read_text())
    for name,digest in manifest["files"].items():
        if hashlib.sha256((bundle/name).read_bytes()).hexdigest()!=digest:raise ValueError(f"contract drift: {name}")
    for name,digest in manifest["python"].items():
        if hashlib.sha256((root/name).read_bytes()).hexdigest()!=digest:raise ValueError(f"SDK drift: {name}")
    for name,model in [("request",AnalysisRequestV1),("response",AnalysisResponseV1),("capabilities",KernelCapabilitiesV1)]:
        if model.model_json_schema()!=json.loads((bundle/f"{name}.schema.json").read_text()):raise ValueError(f"schema drift: {name}")
        model.model_validate_json((bundle/f"{name}.example.json").read_text())
    return manifest["version"]


if __name__=="__main__":print(f"resume-contracts {verify()} verified")
