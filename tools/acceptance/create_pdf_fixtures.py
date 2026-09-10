"""构造无个人数据的 PDF 验收样本；测试依赖 reportlab、pypdf、Pillow，不参与 Go 运行镜像。"""
import argparse
from pathlib import Path
from reportlab.pdfgen import canvas
from reportlab.lib.pagesizes import A4
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont
from reportlab.pdfbase.cidfonts import UnicodeCIDFont
from reportlab.lib.utils import ImageReader
from PIL import Image,ImageDraw
from pypdf import PdfReader,PdfWriter
import json,hashlib
parser=argparse.ArgumentParser(description=__doc__)
parser.add_argument('--output',type=Path,required=True)
parser.add_argument('--font',type=Path)
args=parser.parse_args()
root=args.output
root.mkdir(parents=True,exist_ok=False)
font=args.font
if font and font.exists():pdfmetrics.registerFont(TTFont('Chinese',str(font)))
else:pdfmetrics.registerFont(UnicodeCIDFont('STSong-Light'));font=None
cf='Chinese' if font else 'STSong-Light'
english=['Resume - Text extraction verification','Alex Example | Backend Engineer','Education: Bachelor of Computer Science, 2024','Skills: Go, PostgreSQL, Redis, Docker and React','Experience: Designed reliable background processing services.','Evidence: Implemented cancellation and tested request size limits.']
chinese=['测试简历 - 中文全文验证','张测试 | 后端开发工程师','教育经历：计算机科学与技术本科，2024 年毕业','专业技能：Go、数据库、后台任务及自动化测试','项目经验：设计简历处理服务，保留分页与证据引用。','工作内容：开发任务取消和重试机制，验证完整文本。']
def make(name,pages):
 c=canvas.Canvas(str(root/(name+'.pdf')),pagesize=A4)
 for kind,lines in pages:
  if kind=='scan':c.drawImage(ImageReader(scan),25,30,width=A4[0]-50,height=A4[1]-60)
  elif kind=='blank':pass
  elif kind=='columns':
   c.setFont('Helvetica',10)
   for i,line in enumerate(english):c.drawString(35,790-i*25,line[:47]);c.drawString(320,790-i*25,'Column B: '+str(i+1)+' complete evidence')
  elif kind=='table':
   c.setFont('Helvetica',11)
   for i,cols in enumerate([['Period','Project','Result'],['2023','Go API','Request validation'],['2024','Task queue','Cancellation and retry'],['2025','PDF text','Page and line references']]):
    for j,value in enumerate(cols):c.drawString(40+j*170,780-i*35,value)
    c.line(35,770-i*35,560,770-i*35)
  else:
   c.setFont(cf if kind=='zh' else 'Helvetica',12)
   for i,line in enumerate(lines):c.drawString(40,790-i*28,line)
  c.showPage()
 c.save()
scan=Image.new('RGB',(1200,1680),'white');d=ImageDraw.Draw(scan)
for i,line in enumerate(english):d.text((60,100+i*100),line,fill='black')
make('english',[('en',english)]);make('chinese',[('zh',chinese)]);make('columns',[('columns',[])]);make('table',[('table',[])])
make('blank-middle',[('en',english),('blank',[]),('zh',chinese)])
make('scan',[('scan',[])]);make('mixed',[('en',english),('scan',[])])
make('blank-only',[('blank',[])])
writer=PdfWriter();writer.append(PdfReader(root/'english.pdf'));writer.encrypt('fixture-secret');writer.write(root/'encrypted.pdf')
(root/'damaged.pdf').write_bytes(b'%PDF-1.7\nBroken cross-reference table')
# A valid large PDF: attachment bytes grow the file without changing page text.
writer=PdfWriter();writer.append(PdfReader(root/'english.pdf'))
writer.add_attachment('size-fixture.bin', b'\0'*(32*1024*1024))
writer.write(root/'oversize.pdf')
c=canvas.Canvas(str(root/'overtext.pdf'),pagesize=A4,pageCompression=1)
for page in range(60):
 c.setFont('Helvetica',2)
 for row in range(160):c.drawString(10,810-row*3,('complete text %03d %03d '%(page,row))*8)
 c.showPage()
c.save()
manifest={p.name:{'sha256':hashlib.sha256(p.read_bytes()).hexdigest(),'bytes':p.stat().st_size} for p in root.glob('*.pdf')}
(root/'manifest.json').write_text(json.dumps(manifest,indent=2))
print('Created',len(manifest),'synthetic PDF fixtures')
