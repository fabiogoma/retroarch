# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Fetch missing titles from the user's supplied Internet Archive collection."""
import argparse, hashlib, json, os, urllib.request, urllib.parse, urllib.error, zipfile
from pathlib import Path
from roms import normalized, title_of

def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--root',type=Path,default=Path(__file__).resolve().parent.parent);args=p.parse_args()
 root=args.root.resolve();dest=root/'master system'/'archive.org supplements';dest.mkdir(parents=True,exist_ok=True)
 state=root/'rom_library'/'state';state.mkdir(parents=True,exist_ok=True)
 request=urllib.request.Request('https://archive.org/metadata/master_system',headers={'User-Agent':'PersonalROMLibrary/1.0'})
 with urllib.request.urlopen(request,timeout=60) as f:metadata=json.load(f)
 (state/'master_system_metadata.json').write_text(json.dumps(metadata,indent=2))
 existing={normalized(title_of(p.name)) for p in (root/'master system').rglob('*.zip')}
 log=[]
 for item in metadata['files']:
  name=item['name']
  if not name.lower().endswith('.zip'):continue
  key=normalized(title_of(name))
  if key in existing:continue
  if Path(name).name!=name:raise ValueError('Unsafe remote name: '+name)
  target=dest/name;temp=target.with_name(target.name+'.download')
  try:
   url='https://archive.org/download/master_system/'+urllib.parse.quote(name,safe='')
   print('Downloading',name,flush=True)
   with urllib.request.urlopen(url,timeout=120) as src,temp.open('wb') as dst:
    import shutil
    shutil.copyfileobj(src,dst)
   data=temp.read_bytes()
   if len(data)!=int(item['size']):raise ValueError('Size mismatch')
   if item.get('sha1') and hashlib.sha1(data).hexdigest()!=item['sha1']:raise ValueError('SHA1 mismatch')
   with zipfile.ZipFile(temp) as z:
    if z.testzip():raise ValueError('ZIP integrity failure')
   temp.replace(target);existing.add(key)
   log.append({'file':name,'status':'downloaded','url':url,'sha1':hashlib.sha1(data).hexdigest()})
  except Exception as e:
   log.append({'file':name,'status':'error','error':str(e)});print('ERROR',name,e,flush=True)
   if isinstance(e,urllib.error.HTTPError) and e.code in {401,403}:
    print('Access requires authorization; stopping rather than retrying other files.',flush=True)
    break
  finally:
   if temp.exists():temp.unlink()
 (state/'master_system_downloads.json').write_text(json.dumps(log,indent=2)+'\n')
 print('Downloaded',sum(x['status']=='downloaded' for x in log),'new titles; errors',sum(x['status']=='error' for x in log))
if __name__=='__main__':main()
