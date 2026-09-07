"""外包开发用模拟服务；默认只监听回环地址，不访问文件、模型或数据库。"""
import argparse
import hmac
import json
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from .fixtures import response_fixture, capabilities_fixture
from .models import AnalysisRequestV1


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument("--host",default="127.0.0.1")
    parser.add_argument("--port",type=int,default=8090)
    parser.add_argument("--token",required=True)
    parser.add_argument("--build",default="dev")
    parser.add_argument("--scenario",choices=["success","low_match","failed","budget_exhausted","timeout","invalid_reference","incomplete","invalid_schema"],default="success")
    args=parser.parse_args()
    capabilities = capabilities_fixture(kernel_build=args.build, mock=True)
    class Handler(BaseHTTPRequestHandler):
        def log_message(self,*args): pass
        def reply(self,status,payload):
            raw=json.dumps(payload).encode()
            self.send_response(status);self.send_header("Content-Type","application/json")
            self.send_header("Content-Length",str(len(raw)));self.end_headers();self.wfile.write(raw)
        def do_GET(self):
            if self.path=="/healthz":return self.reply(200,dict(ok=True,mock=True,build=args.build))
            if self.path!="/v2/capabilities":return self.reply(404,{})
            if not hmac.compare_digest(self.headers.get("X-Agent-Kernel-Token",""),args.token):return self.reply(401,{"code":"kernel_unauthorized"})
            self.reply(200,capabilities.model_dump(mode="json"))
        def do_POST(self):
            if self.path!="/v2/tasks/execute":return self.reply(404,{})
            if not hmac.compare_digest(self.headers.get("X-Agent-Kernel-Token",""),args.token):return self.reply(401,{"code":"kernel_unauthorized"})
            try:
                size=int(self.headers.get("Content-Length","0"))
                if size<1 or size>2<<20:return self.reply(413,{})
                request=AnalysisRequestV1.model_validate_json(self.rfile.read(size))
            except (ValueError,TypeError):return self.reply(422,{"code":"invalid_envelope"})
            if any(getattr(request.pin,key)!=getattr(capabilities,key) for key in ("kernel_build","toolset_version","instruction_version")):
                return self.reply(409,{"code":"kernel_version_unavailable"})
            if args.scenario=="timeout":
                time.sleep(2)
                return self.reply(504,{"code":"llm_timeout"})
            self.reply(200,response_fixture(request,args.scenario))
    print(f"MOCK ONLY: http://{args.host}:{args.port} scenario={args.scenario}",flush=True)
    ThreadingHTTPServer((args.host,args.port),Handler).serve_forever()


if __name__=="__main__":main()
