# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Generate RetroArch playlists from export manifests, bypassing on-device scanning."""
import argparse,json
from pathlib import Path,PurePosixPath
NAMES={'nes':'Nintendo - Nintendo Entertainment System','snes':'Nintendo - Super Nintendo Entertainment System','n64':'Nintendo - Nintendo 64','genesis':'Sega - Mega Drive - Genesis','master system':'Sega - Master System - Mark III','PC Engine':'NEC - PC Engine - TurboGrafx 16'}
def main():
 p=argparse.ArgumentParser(description=__doc__);p.add_argument('--device-home',default='~',help='Apple RetroArch home alias (~), or an absolute app-container path');p.add_argument('--export',type=Path,default=Path(__file__).resolve().parent.parent/'ROMs'/'AppleTV-Top40');p.add_argument('--output',type=Path,default=Path(__file__).resolve().parent/'state/apple-tv-playlists');a=p.parse_args()
 home=PurePosixPath(a.device_home)
 if (str(home)!='~' and not home.is_absolute()) or '..' in home.parts:p.error('--device-home must be ~ or an absolute app-container path')
 a.output.mkdir(parents=True,exist_ok=True);report={}
 defaults_file=Path(__file__).resolve().parent/'state/apple-tv-core-defaults.json'
 if not defaults_file.exists():defaults_file=Path(__file__).resolve().parent/'apple_tv_cores.json'
 defaults={e['system']:e for e in json.loads(defaults_file.read_text())}
 for console,name in NAMES.items():
  entries=json.loads((a.export/console/'selection.json').read_text());items=[]
  for e in entries:
   path=str(home/'Library/Caches/RetroArch/downloads/ROMS'/console/e['file'])
   if e['file'].lower().endswith('.zip') and e.get('archive_member'):path+='#'+e['archive_member']
   items.append(dict(path=path,label=e['title'],core_path='DETECT',core_name='DETECT',crc32='DETECT',db_name=name+'.lpl'))
  core=defaults.get(name,{})
  for item in items:
   item['core_path']=core.get('core_path','DETECT');item['core_name']=core.get('core_name','DETECT')
  playlist=dict(version='1.5',default_core_path=core.get('core_path','DETECT'),default_core_name=core.get('core_name','DETECT'),items=items)
  (a.output/(name+'.lpl')).write_text(json.dumps(playlist,indent=2,ensure_ascii=False)+'\n');report[name]=len(items)
 (a.output/'generation.json').write_text(json.dumps({'device_home':str(home),'playlists':report,'note':'Apple RetroArch expands the home alias when loading playlists. Core paths reference framework bundles. Playback must be verified on the Apple TV.'},indent=2)+'\n')
 print(json.dumps(report,indent=2))
if __name__=='__main__':main()
