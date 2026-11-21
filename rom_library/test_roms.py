"""Behavioral tests for archive safety, repeat runs, and export integrity."""
import io,json,sqlite3,tempfile,unittest,zipfile
from pathlib import Path
from unittest.mock import patch
import roms

class LibraryTests(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory();self.root=Path(self.temp.name);self.base=self.root/'rom_library';self.base.mkdir();self.state=self.base/'state';self.state.mkdir();(self.root/'nes').mkdir()
  self.db=sqlite3.connect(':memory:');self.original_base=roms.BASE;roms.BASE=self.base
  self.entry=dict(title='Family Game',aliases=[],play_mode='2 players; simultaneous co-op',family_notes='Cartoon combat',reason='Family play',popularity='Test fixture')
  (self.base/'curation.json').write_text(json.dumps({'nes':[self.entry]}))
 def tearDown(self):
  roms.BASE=self.original_base;self.db.close();self.temp.cleanup()
 def zip(self,name,contents):
  path=self.root/'nes'/name
  with zipfile.ZipFile(path,'w') as z:
   for n,b in contents.items():z.writestr(n,b)
  return path
 def test_single_rom_zip_remains_compressed_and_hashes_rom(self):
  path=self.zip('Family Game (USA).zip',{'Family Game (USA).nes':b'NES\x1a'+b'abc'*100})
  before=path.read_bytes();roms.scan(self.root,self.state,self.db)
  self.assertEqual(self.db.execute('select member from games').fetchone()[0],'Family Game (USA).nes')
  roms.export(self.root,self.state,self.db,30)
  manifest=json.loads((self.root/'AppleTV-Top30/nes/selection.json').read_text())
  self.assertEqual((self.root/'AppleTV-Top30/nes'/manifest[0]['file']).read_bytes(),before)
  self.assertEqual(path.read_bytes(),before)
 def test_nested_bundle_is_extracted_and_incomplete_files_excluded(self):
  inner=io.BytesIO()
  with zipfile.ZipFile(inner,'w') as z:z.writestr('Family Game (USA).nes',b'NES\x1aabc')
  path=self.zip('bundle.zip',{'one.zip':inner.getvalue(),'Second Game (USA).nes':b'NES\x1adef'})
  (self.root/'nes/Unfinished.nes.part').write_bytes(b'partial')
  roms.scan(self.root,self.state,self.db)
  self.assertEqual(self.db.execute('select count(*) from games').fetchone()[0],2)
  self.assertEqual(self.db.execute("select count(*) from files where status='incomplete'").fetchone()[0],1)
  self.assertTrue(path.exists())
 def test_traversal_is_rejected_without_writing(self):
  self.zip('bad.zip',{'../../outside.nes':b'bad','normal.nes':b'good'})
  roms.scan(self.root,self.state,self.db)
  self.assertEqual(self.db.execute('select count(*) from games').fetchone()[0],0)
  self.assertIn('Unsafe member',self.db.execute('select message from issues').fetchone()[0])
  self.assertFalse((self.root/'outside.nes').exists())
 def test_regions_deduplication_and_rerun_remove_stale_game(self):
  usa=self.root/'nes/Family Game (USA).nes';usa.write_bytes(b'usa')
  europe=self.root/'nes/Family Game (Europe).nes';europe.write_bytes(b'eu')
  roms.scan(self.root,self.state,self.db);roms.export(self.root,self.state,self.db,30)
  selection=json.loads((self.root/'AppleTV-Top30/nes/selection.json').read_text());self.assertEqual(len(selection),1);self.assertEqual(selection[0]['source'],str(usa))
  usa.unlink();roms.scan(self.root,self.state,self.db);roms.export(self.root,self.state,self.db,30)
  selection=json.loads((self.root/'AppleTV-Top30/nes/selection.json').read_text());self.assertEqual(selection[0]['source'],str(europe));self.assertEqual(self.db.execute('select count(*) from games').fetchone()[0],1)
 def test_unzip_export_and_failed_export_preserve_previous_set(self):
  self.zip('Family Game.zip',{'Family Game.nes':b'NES\x1a123'})
  roms.scan(self.root,self.state,self.db);roms.export(self.root,self.state,self.db,30,unzip=True)
  selection=json.loads((self.root/'AppleTV-Top30/nes/selection.json').read_text());self.assertTrue(selection[0]['file'].endswith('.nes'))
  out=self.root/'AppleTV-Top30/nes'/selection[0]['file'];self.assertEqual(roms.digest(out),selection[0]['export_sha256'])
  with patch.object(roms.shutil,'copy2',side_effect=OSError('disk full')):
   with self.assertRaises(OSError):roms.export(self.root,self.state,self.db,30)
  self.assertEqual(out.read_bytes(),b'NES\x1a123')
 def test_outer_rar_is_not_mistaken_for_embedded_last_zip(self):
  path=Path(self.original_base).parent/'PC Engine/NEC - PC Engine - TurboGrafx 16 Complete.rar'
  if not path.exists():self.skipTest('Real RAR regression fixture not available')
  with roms.Archive(path) as ar:entries=list(ar)
  self.assertGreater(len(entries),400);self.assertTrue(entries[0][0].endswith('.zip'))

if __name__=='__main__':unittest.main()
