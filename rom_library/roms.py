# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
"""Non-destructive, offline ROM catalog and curated family export."""
from __future__ import annotations
import argparse, csv, ctypes as C, ctypes.util, hashlib, json, os, re, shutil, sqlite3, stat, tempfile, unicodedata, zipfile
from pathlib import Path, PurePosixPath
from datetime import datetime, timezone

BASE = Path(__file__).resolve().parent
ARCHIVES = {'.zip', '.7z', '.rar', '.tar', '.gz', '.bz2', '.xz', '.tgz'}
PROFILES = {name:(set(profile['extensions']), profile['folder_aliases'])
            for name,profile in json.loads((BASE/'consoles.json').read_text()).items()}
ROM_EXTS = set.union(*(v[0] for v in PROFILES.values()))

def normalized(s):
 s = re.sub(r'\([^)]*\)|\[[^]]*\]', '', s)
 s = unicodedata.normalize('NFKD', s).encode('ascii', 'ignore').decode().lower()
 s = s.replace('megaman', 'mega man').replace(' & ', ' and ')
 s = s.replace("'", '')
 s = re.sub(r'\b(\w+)\s+s\b', r'\1s', s)
 s = re.sub(r'[^a-z0-9 ]', ' ', s)
 words = s.split()
 return ' '.join(w for w in words if w not in {'the', 'a', 'an'})

def title_of(name):
 return re.sub(r'\s+', ' ', re.sub(r'\([^)]*\)|\[[^]]*\]', '', Path(name).stem)).strip()

def digest(path):
 h = hashlib.sha256()
 with path.open('rb') as f:
  for b in iter(lambda: f.read(1024*1024), b''): h.update(b)
 return h.hexdigest()

class Archive:
 """Stream libarchive entries; never let the archive choose output paths."""
 def __init__(self, path):
  name = ctypes.util.find_library('archive')
  if not name: raise RuntimeError('Install libarchive (macOS includes it; Linux: libarchive13).')
  self.lib = C.CDLL(name); l = self.lib
  signatures = {
   'archive_read_new': (C.c_void_p, []),
   'archive_read_support_filter_all': (C.c_int, [C.c_void_p]),
   'archive_read_support_format_all': (C.c_int, [C.c_void_p]),
   'archive_read_support_format_rar': (C.c_int, [C.c_void_p]),
   'archive_read_support_format_rar5': (C.c_int, [C.c_void_p]),
   'archive_read_support_format_7zip': (C.c_int, [C.c_void_p]),
   'archive_read_support_format_zip': (C.c_int, [C.c_void_p]),
   'archive_read_open_filename': (C.c_int, [C.c_void_p,C.c_char_p,C.c_size_t]),
   'archive_read_next_header': (C.c_int,[C.c_void_p,C.POINTER(C.c_void_p)]),
   'archive_entry_pathname': (C.c_char_p,[C.c_void_p]),
   'archive_entry_filetype': (C.c_uint,[C.c_void_p]),
   'archive_entry_size': (C.c_longlong,[C.c_void_p]),
   'archive_read_data': (C.c_ssize_t,[C.c_void_p,C.c_void_p,C.c_size_t]),
   'archive_read_free': (C.c_int,[C.c_void_p]),
   'archive_error_string': (C.c_char_p,[C.c_void_p]),
  }
  for n,(result,args) in signatures.items():
   fn = getattr(l,n); fn.restype=result; fn.argtypes=args
  self.a=l.archive_read_new(); l.archive_read_support_filter_all(self.a)
  # Stored nested ZIPs can fool libarchive's format bidding into opening the
  # LAST embedded ZIP rather than the enclosing RAR. Restrict the outer format.
  ext=Path(path).suffix.lower()
  if ext=='.rar': l.archive_read_support_format_rar(self.a); l.archive_read_support_format_rar5(self.a)
  elif ext=='.7z': l.archive_read_support_format_7zip(self.a)
  elif ext=='.zip': l.archive_read_support_format_zip(self.a)
  else: l.archive_read_support_format_all(self.a)
  if l.archive_read_open_filename(self.a,os.fsencode(path),1024*1024) != 0:
   error=l.archive_error_string(self.a); self.close(); raise RuntimeError(str(error))
 def __enter__(self): return self
 def close(self):
  if self.a: self.lib.archive_read_free(self.a); self.a=None
 def __exit__(self,*args): self.close()
 def __iter__(self):
  while True:
   e=C.c_void_p(); rc=self.lib.archive_read_next_header(self.a,C.byref(e))
   if rc==1: break
   if rc<0: raise RuntimeError(str(self.lib.archive_error_string(self.a)))
   name=os.fsdecode(self.lib.archive_entry_pathname(e)); kind=self.lib.archive_entry_filetype(e)
   if kind==stat.S_IFDIR: continue
   if kind!=stat.S_IFREG: raise ValueError('Archive contains a link or special entry: '+name)
   yield name,self.lib.archive_entry_size(e)
 def copy(self,out):
  buffer=C.create_string_buffer(1024*1024)
  while True:
   n=self.lib.archive_read_data(self.a,buffer,len(buffer))
   if n<0: raise RuntimeError(str(self.lib.archive_error_string(self.a)))
   if n==0: break
   out.write(buffer.raw[:n])

def safe_member(name):
 p=PurePosixPath(name.replace('\\','/'))
 if p.is_absolute() or '..' in p.parts or not p.parts or ':' in p.parts[0]: raise ValueError('Unsafe member: '+name)
 return Path(*p.parts)

def prepare_archive(path,state,db,depth=0):
 if depth>8: raise ValueError('Nested archive depth exceeds 8')
 sha=digest(path); dest=state/'extracted'/sha
 row=db.execute('SELECT kind,members FROM archives WHERE sha256=?',(sha,)).fetchone()
 if row and (row[0]=='single_zip' or (dest/'.complete.json').exists()):
  members=json.loads(row[1]); kind=row[0]
 else:
  with Archive(path) as ar: members=[{'name':n,'size':s} for n,s in ar]
  for m in members: safe_member(m['name'])
  if sum(m['size'] for m in members)>32*1024**3: raise ValueError('Archive exceeds 32 GiB extraction limit')
  kind='single_zip' if path.suffix.lower()=='.zip' and len(members)==1 and Path(members[0]['name']).suffix.lower() in ROM_EXTS else 'extract'
  db.execute('INSERT OR REPLACE INTO archives VALUES (?,?,?,?)',(sha,str(path),kind,json.dumps(members)))
 if kind=='single_zip': return [(path,members[0]['name'])]
 if not (dest/'.complete.json').exists():
  dest.parent.mkdir(parents=True,exist_ok=True)
  temp=Path(tempfile.mkdtemp(prefix='unpack-',dir=dest.parent))
  try:
   print('Extracting',path,flush=True)
   total=0
   with Archive(path) as ar:
    for name,size in ar:
     target=temp/safe_member(name); target.parent.mkdir(parents=True,exist_ok=True)
     with target.open('wb') as f: ar.copy(f)
     total+=target.stat().st_size
     if total>32*1024**3: raise ValueError('Archive expanded beyond limit')
   (temp/'.complete.json').write_text(json.dumps({'source':str(path),'sha256':sha}))
   if dest.exists(): shutil.rmtree(dest)
   temp.rename(dest)
  finally:
   if temp.exists(): shutil.rmtree(temp)
 result=[]
 for p in sorted(dest.rglob('*')):
  if p.is_file() and not p.is_symlink():
   if p.suffix.lower() in ARCHIVES: result.extend(prepare_archive(p,state,db,depth+1))
   elif p.suffix.lower() in ROM_EXTS: result.append((p,''))
 return result

def console_for(folder,ext):
 for console,(extensions,aliases) in PROFILES.items():
  if folder.lower() in aliases: return console
 matches=[c for c,(es,_) in PROFILES.items() if ext in es]
 return matches[0] if len(matches)==1 else folder

def region_score(name):
 score=0
 if re.search(r'\(USA|\(U\)|Japan, USA',name,re.I): score+=30
 elif re.search(r'\(World|\(W\)',name,re.I): score+=25
 elif re.search(r'Europe|\(E\)',name,re.I): score+=20
 if re.search(r'\(En(?:[,)]|\))',name): score+=5
 if re.search(r'Japan|\(J\)',name) and not re.search(r'USA|World|En[,)]',name): score-=30
 if re.search(r'Beta|Proto|Sample|\(Unl\)|\[b\d|\[h\d|\[t\d',name,re.I): score-=100
 return score

def scan(root,state,db):
 db.executescript('''CREATE TABLE IF NOT EXISTS archives(sha256 TEXT PRIMARY KEY,path TEXT,kind TEXT,members TEXT);
 CREATE TABLE IF NOT EXISTS files(path TEXT PRIMARY KEY,console TEXT,kind TEXT,bytes INTEGER,mtime_ns INTEGER,status TEXT);
 CREATE TABLE IF NOT EXISTS games(id INTEGER PRIMARY KEY,console TEXT,title TEXT,title_key TEXT,path TEXT,member TEXT,sha256 TEXT,bytes INTEGER,region_score INTEGER,UNIQUE(console,path,member));
 CREATE TABLE IF NOT EXISTS selections(console TEXT,rank INTEGER,game_id INTEGER,title TEXT,details TEXT,PRIMARY KEY(console,rank));
 CREATE TABLE IF NOT EXISTS issues(path TEXT,message TEXT);
 CREATE VIEW IF NOT EXISTS titles AS SELECT console,title_key,MIN(title) AS title,COUNT(*) AS variants,COUNT(DISTINCT sha256) AS distinct_dumps FROM games GROUP BY console,title_key;
 ''')
 db.execute('DELETE FROM files');db.execute('DELETE FROM games');db.execute('DELETE FROM issues');db.execute('DELETE FROM selections')
 seen=set()
 for folder in sorted(root.iterdir()):
  if not folder.is_dir() or folder.is_symlink() or folder.name.startswith('.') or (folder.name=='rom_library' or re.fullmatch(r'AppleTV-Top[0-9]+',folder.name)): continue
  for path in sorted(folder.rglob('*')):
   if not path.is_file() or path.is_symlink(): continue
   ext=path.suffix.lower(); status='incomplete' if ext in {'.part','.crdownload','.download'} else 'present'
   kind='archive' if ext in ARCHIVES else 'rom' if ext in ROM_EXTS else 'auxiliary'
   st=path.stat();db.execute('INSERT INTO files VALUES (?,?,?,?,?,?)',(str(path),folder.name,kind,st.st_size,st.st_mtime_ns,status))
   if kind=='auxiliary': continue
   try:
    candidates=prepare_archive(path,state,db) if kind=='archive' else [(path,'')]
    for p,member in candidates:
     name=member or p.name; console=console_for(folder.name,Path(name).suffix.lower())
     identity=(console,str(p),member)
     if identity in seen: continue
     seen.add(identity)
     if member:
      with zipfile.ZipFile(p) as z:
       h=hashlib.sha256();size=0
       with z.open(member) as f:
        for b in iter(lambda:f.read(1024*1024),b''):h.update(b);size+=len(b)
       sha=h.hexdigest()
     else: sha=digest(p);size=p.stat().st_size
     db.execute('INSERT OR IGNORE INTO games(console,title,title_key,path,member,sha256,bytes,region_score) VALUES (?,?,?,?,?,?,?,?)',(console,title_of(name),normalized(title_of(name)),str(p),member,sha,size,region_score(name)))
   except Exception as e:
    print('ISSUE',path,e,flush=True);db.execute('INSERT INTO issues VALUES (?,?)',(str(path),str(e)))
 db.commit()

def export(root,state,db,count,unzip=False):
 curated=json.loads((BASE/'curation.json').read_text())
 target=root/f'AppleTV-Top{count}'; stage=Path(tempfile.mkdtemp(prefix='export-',dir=state))
 report=[];missing=[]
 try:
  consoles=sorted({r[0] for r in db.execute('SELECT DISTINCT console FROM games')} | set(curated))
  for console in consoles:
   folder=stage/console;folder.mkdir()
   rows=db.execute('SELECT * FROM games WHERE console=?',(console,)).fetchall()
   columns=[d[1] for d in db.execute('PRAGMA table_info(games)')];games=[dict(zip(columns,r)) for r in rows]
   ranked=[];used=set()
   for entry in curated.get(console,[]):
    keys={normalized(a) for a in [entry['title']]+entry.get('aliases',[])}
    candidates=[g for g in games if g['title_key'] in keys and (g['region_score']>=0 or entry.get('allow_japanese',False) and g['region_score']>-100) and g['sha256'] not in used]
    if not candidates:
     missing.append({'console':console,'title':entry['title'],'reason':'No matching complete preferred-language ROM'});continue
    best=max(candidates,key=lambda g:(g['region_score'],not bool(g['member']),g['path']))
    if len(ranked)>=count: continue
    ranked.append((entry,best));used.add(best['sha256'])
   manifest=[]
   for rank,(entry,g) in enumerate(ranked,1):
    src=Path(g['path']);suffix=Path(g['member']).suffix.lower() if unzip and g['member'] else src.suffix.lower(); filename=re.sub(r'[/\\:*?"<>|]','-',entry['title'])+suffix
    out=folder/filename
    if unzip and g['member']:
     with zipfile.ZipFile(src) as z,z.open(g['member']) as f,out.open('wb') as dst: shutil.copyfileobj(f,dst)
    else: shutil.copy2(src,out)
    item={'rank':rank,**entry,'file':filename,'source':str(src),'archive_member':g['member'],'language_note':('Japanese source; menus may need help' if g['region_score']<0 else 'Preferred USA/World/Europe variant; language inferred from filename'),'rom_sha256':g['sha256'],'export_sha256':digest(out),'bytes':out.stat().st_size}
    manifest.append(item)
    db.execute('INSERT INTO selections VALUES (?,?,?,?,?)',(console,rank,g['id'],entry['title'],json.dumps(item)))
   (folder/'selection.json').write_text(json.dumps(manifest,indent=2,ensure_ascii=False)+'\n')
   lines=[f'# {console}: {len(manifest)} / {count} available picks','', 'Order is a personal recommendation, not a sales chart. Notes are parental guidance, not age ratings.','', '| # | Game | Play mode | Family notes | Why selected | Popularity evidence |','|---|---|---|---|---|---|']
   for item in manifest:lines.append(f"| {item['rank']} | {item['title']} | {item['play_mode']} | {item['family_notes']} | {item['reason']} | {item['popularity']} |")
   (folder/f'TOP{count}.md').write_text('\n'.join(lines)+'\n')
   report.append({'console':console,'catalog_variants':len(games),'unique_titles':len({g['title_key'] for g in games}),'selected':len(manifest),'target':count,'bytes':sum(x['bytes'] for x in manifest)})
  summary={'generated_utc':datetime.now(timezone.utc).isoformat(),'consoles':report,'missing_curated':missing,'issues':[dict(zip(['path','message'],r)) for r in db.execute('SELECT * FROM issues')],'incomplete_files':[r[0] for r in db.execute("SELECT path FROM files WHERE status='incomplete'")], 'download_log':str(state/'master_system_downloads.json'), 'unreviewed_consoles':sorted(set(consoles)-set(curated)), 'selection_policy':'Editorial order combining published popularity evidence with family/taste fit; not a sales chart. New consoles need a reviewed curation.json list.'}
  (stage/'summary.json').write_text(json.dumps(summary,indent=2)+'\n')
  (stage/'README.md').write_text(f'# Apple TV upload set\n\nTo transfer missing ROMs, run `uv run --no-project rom_library/sync_apple_tv.py --export ROMs/AppleTV-Top{count}` from the project root. For manual uploads, transfer only the ROM files inside each console folder using RetroArch’s web file server. Keep this folder structure. Do not upload the catalog, original collection bundles or unfinished downloads. On Apple TV, use Import Content → Manual Scan, select the console folder and system, and enable Scan Inside Archives for ZIPs. Archive content is one game per ZIP. If a core cannot load a ZIP, rerun with --unzip-export.\n\nRecommended cores: NES: FCEUmm or Nestopia; SNES: Snes9x; Genesis and Master System: Genesis Plus GX; PC Engine: Beetle PCE FAST; N64: an available N64 core. Core availability and N64 performance depend on the Apple TV build/device; no device play test was performed.\n\nTOP{count}.md and selection.json describe every pick. Missing games are in summary.json; no padding with unsuitable or unreviewed games. At age two, treat these as parent-led watching/brief controller play; colorful games can still be difficult. Preview fantasy combat and spooky scenes before family play.\n')
  backup=state/'previous-export'
  if backup.exists(): shutil.rmtree(backup)
  if target.exists(): target.rename(backup)
  try: stage.rename(target)
  except Exception:
   if backup.exists(): backup.rename(target)
   raise
  db.commit()
  print(json.dumps(report,indent=2))
 finally:
  if stage.exists():shutil.rmtree(stage)

def main():
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--root',type=Path,default=BASE.parent,help='Project root (state)');parser.add_argument('--roms',type=Path,default=BASE.parent/'ROMs',help='ROMs folder holding console folders and exports');parser.add_argument('--count',type=int,default=40);parser.add_argument('--unzip-export',action='store_true');args=parser.parse_args()
 if args.count<1:parser.error('--count must be positive')
 root=args.root.resolve();state=root/'rom_library'/'state';state.mkdir(parents=True,exist_ok=True)
 # Refuse concurrent runs rather than race over extraction/export directories.
 import fcntl
 with (state/'run.lock').open('w') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  with sqlite3.connect(state/'catalog.sqlite') as db:
   roms=args.roms.resolve();scan(roms,state,db);export(roms,state,db,args.count,args.unzip_export)
   with (state/'catalog.csv').open('w',newline='') as f:
    writer=csv.writer(f);query=db.execute('SELECT * FROM games ORDER BY console,title');writer.writerow([d[0] for d in query.description]);writer.writerows(query)
   with (state/'titles.csv').open('w',newline='') as f:
    writer=csv.writer(f);query=db.execute('SELECT * FROM titles ORDER BY console,title');writer.writerow([d[0] for d in query.description]);writer.writerows(query)
if __name__=='__main__':main()
