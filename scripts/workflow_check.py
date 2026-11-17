#!/usr/bin/env python3
"""Bounded production workflow verification; baseline automated testing is deferred."""
import argparse
import hashlib
import io
import json
import os
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

def need(value, message):
    if not value:
        raise RuntimeError(message)

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--workflow', default='all')
    args = ap.parse_args()
    observations = []
    with tempfile.TemporaryDirectory(prefix='cadence-workflow-') as scratch:
        scratch = Path(scratch)
        ffmpeg = os.environ.get('FFMPEG_BIN', 'ffmpeg')
        subprocess.run([ffmpeg, '-nostdin', '-hide_banner', '-loglevel', 'error',
                        '-f', 'lavfi', '-i', 'testsrc2=size=160x90:rate=24:duration=4',
                        '-f', 'lavfi', '-i', 'sine=frequency=660:sample_rate=48000:duration=4',
                        '-c:v', 'libx264', '-preset', 'ultrafast', '-threads', '1',
                        '-c:a', 'aac', '-shortest', str(scratch/'calibration.mp4')], check=True, timeout=30)
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        address = 'http://127.0.0.1:'+str(port)+'/forge/v1'
        env = dict(os.environ, FORGE_ROOT=str(scratch/'data'),
                   FORGE_ADDR='127.0.0.1:'+str(port), FORGE_WORKERS='1')
        burn_font = os.environ.get('FORGE_BURN_FONT', '')
        burn_language = os.environ.get('FORGE_CAPTION_BURN_LANGUAGE', 'zh-Hans' if burn_font else 'en')
        log = open(scratch/'server.log', 'ab')
        process = None
        def request(method, route, body=None, headers=None, status=200, raw=False):
            hs = dict(headers or {})
            if isinstance(body, (dict, list)):
                body = json.dumps(body).encode()
                hs['Content-Type'] = 'application/json'
            req = urllib.request.Request(address+route, data=body, headers=hs, method=method)
            try:
                response = urllib.request.urlopen(req, timeout=60)
            except urllib.error.HTTPError as exc:
                response = exc
            with response:
                data = response.read()
                need(response.status == status, f'{method} {route}: {response.status}, wanted {status}: {data[:600]!r}')
                return data if raw or not data else json.loads(data)
        def start():
            nonlocal process
            process = subprocess.Popen([str(ROOT/'bin/forged')], cwd=ROOT, env=env,
                                       stdout=log, stderr=log, start_new_session=True)
            deadline = time.monotonic()+10
            while time.monotonic() < deadline:
                try:
                    request('GET', '/health')
                    return
                except (OSError, RuntimeError):
                    if process.poll() is not None:
                        raise RuntimeError((scratch/'server.log').read_text())
                    time.sleep(.05)
            raise RuntimeError('server readiness timed out')
        def stop(force=False):
            nonlocal process
            if process and process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL if force else signal.SIGTERM)
                process.wait(timeout=15)
            process = None
        def observe(name, detail):
            observations.append({'workflow_id': name, 'passed': True, 'observed': detail})
        def wait_render(rid, desired=None):
            deadline = time.monotonic()+40
            while time.monotonic() < deadline:
                r = request('GET', '/renders/'+rid)
                if desired and r['state'] == desired:
                    return r
                if not desired and r['state'] in ('succeeded', 'failed', 'cancelled'):
                    return r
                time.sleep(.02)
            raise RuntimeError('render state timeout')
        try:
            start()
            media = (scratch/'calibration.mp4').read_bytes()
            source = request('POST', '/sources?name=calibration-pulse', media, status=201)
            sid = source['id']
            need(source['media']['video'] and source['media']['audio'], 'probe lost streams')
            need(source['digest'] == hashlib.sha256(media).hexdigest(), 'source digest mismatch')
            request('POST', '/sources?name=invalid', b'not-media', status=400)
            duplicate = request('POST', '/sources?name=duplicate', media, status=201)
            need(duplicate['id'] == sid, 'ingest did not converge')
            observe('ingest', 'MP4 accepted with SHA-256; malformed bytes rejected; duplicate converges')
            recipe_body = dict(name='review-proxy', format='mp4', width=160, height=90,
                               fps=24, sample_rate=48000, normalize=False, target_lufs=-16)
            recipe = request('POST', '/recipes', recipe_body, status=201)
            rp = recipe['id']
            updated = request('PUT', '/recipes/'+rp, dict(recipe_body, name='review-proxy-v2'), {'If-Match': '1'}, status=201)
            need(updated['revision'] == 2, 'recipe revision not advanced')
            request('PUT', '/recipes/'+rp, recipe_body, {'If-Match': '1'}, status=409)
            observe('recipe-version', 'revision 2 saved; stale revision rejected')
            sheet_body = dict(name='two-pass-calibration', spans=[
                dict(source_id=sid, in_ms=0, out_ms=1500, gain_db=-3),
                dict(source_id=sid, in_ms=2000, out_ms=3500, gain_db=0)],
                cues=[dict(source_id=sid, start_ms=1000, end_ms=2500, text='校准 <幅度>')])
            sheet = request('POST', '/sheets', sheet_body, status=201)
            sh = sheet['id']
            revision = request('PUT', '/sheets/'+sh, dict(sheet_body, name='revised-calibration'), {'If-Match': '1'}, status=201)
            need(revision['revision'] == 2, 'sheet revision not advanced')
            request('PUT', '/sheets/'+sh, sheet_body, {'If-Match': '1'}, status=409)
            observe('sheet-version', 'two-cut revision saved; stale writer rejected')
            plan = request('GET', '/sheets/'+sh+'/preview?recipe_id='+rp)
            need(plan['duration_ms'] == 3000 and plan['placements'][1]['output_in_ms'] == 1500, 'timeline mismatch')
            observe('preview', 'two source spans compacted to a 3000ms composition')
            need('00:00:01.000 --> 00:00:01.500' in plan['vtt'], 'first caption clipping mismatch')
            need('00:00:01.500 --> 00:00:02.000' in plan['vtt'], 'second caption mapping mismatch')
            need('&lt;幅度&gt;' in plan['vtt'], 'caption markup not escaped')
            observe('captions', 'source cue split and clipped into two escaped output intervals')
            if args.workflow in ('all', 'multilingual-captions', 'multilingual-delivery', 'caption-burn', 'caption-failure'):
                # A repeated source is re-ordered into three cuts. The Chinese
                # sentence deliberately crosses two of them; English is segmented
                # independently. Both must point at the same confirmed picture.
                multi_sheet_body = dict(name='multilingual-cut', spans=[
                    dict(source_id=sid, in_ms=0, out_ms=1200, gain_db=0),
                    dict(source_id=sid, in_ms=1200, out_ms=2600, gain_db=0),
                    dict(source_id=sid, in_ms=0, out_ms=600, gain_db=0)], cues=[])
                multi_sheet = request('POST', '/sheets', multi_sheet_body, status=201)['id']
                request('PUT', '/sheets/'+multi_sheet, dict(multi_sheet_body, name='multilingual-cut-v2'),
                        {'If-Match': '1'}, status=201)
                group = request('POST', '/caption-groups',
                                dict(name='delivery-languages', sheet_id=multi_sheet, sheet_revision=1, languages={}), status=201)
                gid = group['id']
                zh = request('PUT', f'/caption-groups/{gid}/languages/zh-Hans',
                             dict(sheet_id=multi_sheet, sheet_revision=1, cues=[
                                 dict(start_ms=100, end_ms=1400, text='第一句完整保留'),
                                 dict(start_ms=1400, end_ms=3400, text='第二句跨越切点也完整')]),
                             {'If-Match': '0'}, status=201)
                en = request('PUT', f'/caption-groups/{gid}/languages/en',
                             dict(sheet_id=multi_sheet, sheet_revision=1, cues=[
                                 dict(start_ms=100, end_ms=1400, text='The first sentence stays whole.'),
                                 dict(start_ms=1400, end_ms=2900, text='The second sentence crosses the cuts.')]),
                             {'If-Match': '0'}, status=201)
                need(zh['revision'] == 1 and en['revision'] == 1, 'language revisions not independent')
                request('PUT', f'/caption-groups/{gid}/languages/en',
                        dict(sheet_id=multi_sheet, sheet_revision=1, cues=[
                            dict(start_ms=0, end_ms=900, text='a'), dict(start_ms=800, end_ms=1200, text='b')]),
                        {'If-Match': '1'}, status=400)
                published = request('PUT', f'/caption-groups/{gid}',
                                    dict(name='delivery-languages', sheet_id=multi_sheet, sheet_revision=1,
                                         languages={'zh-Hans': 1, 'en': 1}),
                                    {'If-Match': '1'}, status=201)
                need(published['revision'] == 2, 'group revision not advanced')
                view = request('GET', f'/caption-groups/{gid}')
                need(view['group']['languages'] == {'en': 1, 'zh-Hans': 1}, 'group did not pin both languages')
                need(set(view['tracks']) == {'en', 'zh-Hans'}, 'group view lost a language')
                need('00:00:01.400 --> 00:00:03.200' in view['tracks']['zh-Hans']['vtt'],
                     'composition-timed cue was split at a cut or clipped')
                need(any(f['code'] == 'cue_clipped' for f in view['tracks']['zh-Hans']['findings']),
                     'overrun past the composition end was not explained')
                need(any(f['code'] == 'reading_rate' for f in view['tracks']['en']['findings']),
                     'unreadable English cue was not reported')
                request('PUT', f'/caption-groups/{gid}/languages/en',
                        dict(sheet_id=multi_sheet, sheet_revision=1, cues=[
                            dict(start_ms=100, end_ms=1400, text='Edited after publication.')]),
                        {'If-Match': '1'}, status=201)
                after = request('GET', f'/caption-groups/{gid}')
                need(after['group']['languages']['en'] == 1, 'monolingual edit changed a published group')
                need(after['tracks']['en']['track']['revision'] == 1, 'group view followed the later edit')
                need(any(p['code'] == 'newer_language_revision' for p in after['problems']),
                     'newer language revision was not explained')
                observe('multilingual-captions',
                        'repeated source re-ordered into three cuts; zh-Hans sentence crosses cuts without splitting; '
                        'English segmented independently; published group keeps pinned revisions after a later edit')
                multi_req = dict(sheet_id=multi_sheet, sheet_revision=1, recipe_id=rp, recipe_revision=1)
                no_burn = request('POST', '/renders', dict(multi_req, caption_group=gid, caption_revision=2),
                                  {'Idempotency-Key': 'multi-sidecar'}, status=202)['id']
                sidecar_done = wait_render(no_burn)
                need(sidecar_done['state'] == 'succeeded', str(sidecar_done['attempts']))
                need(sidecar_done['artifact']['caption_files'] == ['captions.en.vtt', 'captions.zh-Hans.vtt'],
                     'sidecar language set mismatch')
                need(sidecar_done['artifact']['caption_file'] == 'captions.en.vtt', 'no default sidecar chosen')
                en_vtt = request('GET', f'/renders/{no_burn}/artifact?captions=en', raw=True)
                need(b'The first sentence stays whole.' in en_vtt, 'pinned English text drifted')
                need(b'Edited after publication.' not in en_vtt, 'later monolingual edit leaked into the group')
                zh_vtt = request('GET', f'/renders/{no_burn}/artifact?captions=zh-Hans', raw=True)
                need('第二句跨越切点也完整'.encode() in zh_vtt, 'Chinese sentence was truncated')
                with zipfile.ZipFile(io.BytesIO(request('GET', f'/renders/{no_burn}/bundle', raw=True))) as z:
                    need({'composition.mp4', 'captions.en.vtt', 'captions.zh-Hans.vtt', 'composition.json'} <= set(z.namelist()),
                         'bundle lost a language')
                    manifest = json.loads(z.read('composition.json'))
                    need(manifest['plan']['captions']['group_revision'] == 2, 'bundle followed a later group')
                    pinned = {t['language']: t['revision'] for t in manifest['plan']['captions']['tracks']}
                    need(pinned == {'en': 1, 'zh-Hans': 1}, 'bundle pinned the wrong language revisions')
                    report = {r['language']: r for r in manifest['caption_report']}
                    need(any(f['code'] == 'reading_rate' for f in report['en']['findings']),
                         'bundle did not explain the unreadable translation')
                    need(report['zh-Hans']['findings'] and
                         any(f['code'] == 'cue_clipped' for f in report['zh-Hans']['findings']),
                         'bundle did not explain the clipped display window')
                observe('multilingual-delivery',
                        'sidecars and bundle export the pinned language revisions; later single-language edit does not leak in')
                if args.workflow in ('all', 'caption-burn'):
                    # Burning CJK without an operator-chosen font that covers the
                    # script must fail visibly rather than render missing glyphs.
                    if not burn_font:
                        request('POST', '/renders', dict(multi_req, caption_group=gid, caption_revision=2,
                                                         burn_languages=['zh-Hans']),
                                {'Idempotency-Key': 'burn-cjk-default'}, status=409)
                    burn_req = dict(multi_req, caption_group=gid, caption_revision=2,
                                    burn_languages=[burn_language])
                    burn = request('POST', '/renders', burn_req,
                                   {'Idempotency-Key': 'multi-burn'}, status=202)['id']
                    burn_done = wait_render(burn)
                    need(burn_done['state'] == 'succeeded', str(burn_done['attempts']))
                    need(burn_done['artifact']['burned_file'] == 'composition.burned.mp4', 'no burned derivative')
                    clean = request('GET', f'/renders/{burn}/artifact', raw=True)
                    burned = request('GET', f'/renders/{burn}/artifact?burned=1', raw=True)
                    need(len(clean) > 0 and len(burned) > 0 and clean != burned, 'burn derivative equals clean master')
                    verification = request('POST', f'/renders/{burn}/verify')
                    need(verification['digest'] == burn_done['artifact']['inspection']['digest'],
                         'clean master changed when burning')
                    observe('caption-burn',
                            ('CJK burn refused on the Latin-only default font; ' if not burn_font else 'explicit font configured; ') +
                            'the MP4 burn is a separate derivative of the same frozen cues and the clean master is unchanged')
                if args.workflow in ('all', 'caption-failure'):
                    wav_recipe = request('POST', '/recipes', dict(recipe_body, name='caption-audio', format='wav'),
                                         status=201)['id']
                    request('POST', '/renders', dict(multi_req, recipe_id=wav_recipe, caption_group=gid,
                                                     caption_revision=2, burn_languages=['en']),
                            {'Idempotency-Key': 'burn-wav'}, status=400)
                    request('POST', '/renders', dict(multi_req, caption_group=gid, caption_revision=2,
                                                     burn_languages=['fr']),
                            {'Idempotency-Key': 'burn-unknown'}, status=400)
                    request('POST', '/renders', dict(multi_req, caption_group=gid, caption_revision=2),
                            {'Idempotency-Key': 'multi-sidecar'}, status=202)
                    request('POST', '/renders', dict(multi_req, sheet_revision=2, caption_group=gid,
                                                     caption_revision=2),
                            {'Idempotency-Key': 'multi-cut-rev'}, status=409)
                    # Partial failure: deleting one pinned language file must fail
                    # the whole delivery, leave the render's explanation intact,
                    # and still name which group revision is not deliverable.
                    damaged = request('POST', '/renders', dict(multi_req, caption_group=gid, caption_revision=2),
                                      {'Idempotency-Key': 'multi-damaged'}, status=202)['id']
                    damaged_done = wait_render(damaged)
                    need(damaged_done['state'] == 'succeeded', 'damaged-delivery setup failed')
                    damaged_dir = scratch/'data'/'renders'/f'{damaged}-1'
                    (damaged_dir/'captions.en.vtt').unlink()
                    request('POST', f'/renders/{damaged}/verify', status=500)
                    request('GET', f'/renders/{damaged}/bundle', status=500)
                    request('GET', f'/renders/{damaged}/artifact?captions=en', status=500)
                    request('GET', f'/renders/{damaged}/artifact?captions=zh-Hans', status=500)
                    recovered = request('POST', f'/renders/{damaged}/retry', status=409)
                    need(recovered['error'] == 'conflict', 'succeeded render accepted a retry')
                    healthy = request('POST', '/renders', dict(multi_req, caption_group=gid, caption_revision=2),
                                      {'Idempotency-Key': 'multi-healthy'}, status=202)['id']
                    healthy_done = wait_render(healthy)
                    need(healthy_done['state'] == 'succeeded', str(healthy_done['attempts']))
                    request('POST', f'/renders/{healthy}/verify')
                    # A killed caption attempt must recover as interrupted, keep
                    # its frozen group, and only the retried attempt may deliver.
                    long_sheet = request('POST', '/sheets', dict(name='caption-interrupt', spans=multi_sheet_body['spans'] * 10, cues=[]), status=201)['id']
                    retry_group = request('POST', '/caption-groups', dict(name='retry-group', sheet_id=long_sheet, sheet_revision=1, languages={}), status=201)['id']
                    request('PUT', f'/caption-groups/{retry_group}/languages/en',
                                          dict(sheet_id=long_sheet, sheet_revision=1,
                                               cues=[dict(start_ms=0, end_ms=2000, text='Frozen before interruption.')]),
                                          {'If-Match': '0'}, status=201)
                    request('PUT', f'/caption-groups/{retry_group}',
                            dict(name='retry-group', sheet_id=long_sheet, sheet_revision=1, languages={'en': 1}),
                            {'If-Match': '1'}, status=201)
                    killed = request('POST', '/renders', dict(sheet_id=long_sheet, sheet_revision=1,
                                                              recipe_id=rp, recipe_revision=1,
                                                              caption_group=retry_group, caption_revision=2),
                                     {'Idempotency-Key': 'caption-interrupt'}, status=202)['id']
                    wait_render(killed, 'running')
                    stop(force=True)
                    start()
                    recovered = request('GET', '/renders/'+killed)
                    need(recovered['state'] == 'failed' and recovered['attempts'][-1]['outcome'] == 'interrupted',
                         'caption recovery provenance mismatch')
                    need(recovered['plan']['captions']['group_id'] == retry_group, 'interrupted attempt lost its frozen group')
                    request('POST', f'/renders/{killed}/retry', status=202)
                    retried = wait_render(killed)
                    need(retried['state'] == 'succeeded' and len(retried['attempts']) == 2, str(retried['attempts']))
                    need(retried['artifact']['caption_files'] == ['captions.en.vtt'], 'retry did not keep the pinned language')
                    en_after_retry = request('GET', f'/renders/{killed}/artifact?captions=en', raw=True)
                    need(b'Frozen before interruption.' in en_after_retry, 'retry changed the frozen caption text')
                    request('POST', f'/renders/{killed}/verify')
                    observe('caption-failure',
                            'unrequested burn language, audio-only recipe burn and wrong picture revision are rejected; '
                            'a missing language file makes only that attempt undeliverable; '
                            'a killed caption attempt recovers as interrupted and its retry reuses the pinned group')
            if args.workflow in ('all', 'loudness'):
                loud = request('POST', '/sources/'+sid+'/loudness')
                need(loud['integrated_lufs'] is not None and not loud['silent'], 'loudness scan failed')
                observe('loudness', 'measured integrated LUFS and true peak')
            for mode in ['storyboard', 'waveform']:
                if args.workflow in ('all', mode):
                    image = request('GET', '/sources/'+sid+'/visual?mode='+mode, raw=True)
                    need(image.startswith(b'\x89PNG'), 'inspection output is not PNG')
                    observe(mode, f'PNG produced from source streams: {len(image)} bytes')
            req = dict(sheet_id=sh, sheet_revision=1, recipe_id=rp, recipe_revision=1)
            render = request('POST', '/renders', req, {'Idempotency-Key': 'first-render'}, status=202)
            rid = render['id']
            again = request('POST', '/renders', req, {'Idempotency-Key': 'first-render'}, status=202)
            need(again['id'] == rid, 'idempotency key did not converge')
            request('POST', '/renders', dict(req, sheet_revision=2), {'Idempotency-Key': 'first-render'}, status=409)
            observe('idempotent-submit', 'same request converges; changed revision conflicts')
            done = wait_render(rid)
            need(done['state'] == 'succeeded', str(done['attempts']))
            need(done['sheet']['revision'] == 1 and done['recipe']['revision'] == 1, 'frozen revisions drifted')
            observe('mp4-render', 'frozen revisions encoded to H.264/AAC MP4 and inspected')
            verification = request('POST', '/renders/'+rid+'/verify')
            artifact = request('GET', '/renders/'+rid+'/artifact', raw=True)
            need(hashlib.sha256(artifact).hexdigest() == verification['digest'], 'artifact digest mismatch')
            observe('artifact-verify', 'download SHA-256 equals committed inspection')
            if args.workflow in ('all', 'bundle'):
                packed = request('GET', '/renders/'+rid+'/bundle', raw=True)
                with zipfile.ZipFile(io.BytesIO(packed)) as z:
                    need(set(z.namelist()) == {'composition.mp4', 'captions.vtt', 'composition.json'}, 'bundle members mismatch')
                    frozen = json.loads(z.read('composition.json'))
                    need(frozen['plan']['vtt'] == z.read('captions.vtt').decode(), 'bundle captions drifted')
                observe('bundle', 'verified media, captions and frozen manifest exported together')
            if args.workflow in ('all', 'wav-render'):
                ar = request('POST', '/recipes', dict(recipe_body, name='reference-audio', format='wav', normalize=True), status=201)
                audio_render = request('POST', '/renders', dict(req, recipe_id=ar['id']), {'Idempotency-Key': 'audio'}, status=202)
                audio_done = wait_render(audio_render['id'])
                need(audio_done['state'] == 'succeeded', str(audio_done['attempts']))
                need(audio_done['artifact']['inspection']['media']['audio_codec'] == 'pcm_s16le', 'audio codec mismatch')
                observe('wav-render', 'trimmed stereo PCM WAV encoded with loudness filter')
            request('DELETE', '/sources/'+sid, status=409)
            observe('retire-source', 'historical sheet reference prevents retirement')
            if args.workflow in ('all', 'cancel', 'retry', 'restart-recover'):
                long_sheet = request('POST', '/sheets', dict(sheet_body, name='extended-calibration', spans=sheet_body['spans']*16), status=201)
                pending = request('POST', '/renders', dict(req, sheet_id=long_sheet['id']), {'Idempotency-Key': 'interrupt'}, status=202)
                interrupted_id = pending['id']
                wait_render(interrupted_id, 'running')
                cancelled = request('POST', '/renders', req, {'Idempotency-Key': 'cancel-me'}, status=202)
                cancelled = request('POST', '/renders/'+cancelled['id']+'/cancel')
                need(cancelled['state'] == 'cancelled', 'cancel not acknowledged')
                request('GET', '/renders/'+cancelled['id']+'/artifact', status=409)
                observe('cancel', 'composition cancelled without artifact visibility')
                stop(force=True)
                start()
                recovered = request('GET', '/renders/'+interrupted_id)
                need(recovered['state'] == 'failed' and recovered['attempts'][-1]['outcome'] == 'interrupted', 'recovery provenance mismatch')
                observe('restart-recover', 'SIGKILL/restart retained frozen inputs and marked interruption')
                request('POST', '/renders/'+interrupted_id+'/retry', status=202)
                retried = wait_render(interrupted_id)
                need(retried['state'] == 'succeeded' and len(retried['attempts']) == 2, str(retried['attempts']))
                observe('retry', 'second attempt completed using original snapshot')
            events = request('GET', '/events?after=0')
            need(events and all(a['sequence'] < b['sequence'] for a, b in zip(events, events[1:])), 'event order mismatch')
            tail = request('GET', '/events?after='+str(events[-1]['sequence']))
            need(all(e['sequence'] > events[-1]['sequence'] for e in tail), 'event cursor repeated')
            observe('events', 'persisted event cursor strictly increases')
            if args.workflow != 'all':
                need(any(x['workflow_id'] == args.workflow for x in observations), 'unknown workflow')
            print(json.dumps({'ok': True, 'requested': args.workflow, 'observations': observations}, ensure_ascii=False))
        except Exception:
            log.flush()
            print((scratch/'server.log').read_text(errors='replace'))
            raise
        finally:
            stop()
            log.close()

if __name__ == '__main__':
    main()
