#!/usr/bin/env python3
"""Real CLI and read API against a local minute provider; no credentials or live traffic."""
import argparse
from datetime import datetime, timedelta
from zoneinfo import ZoneInfo
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import socket
import sqlite3
import subprocess
import tempfile
import threading
import time
from urllib.parse import urlparse, parse_qs
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]
STATE = {"mode": "ok", "daily": 0, "minute": 0}
TZ = ZoneInfo("Asia/Shanghai")


def target():
    now = datetime.now(TZ)
    day = now.replace(hour=0, minute=0, second=0, microsecond=0)
    n = now.hour * 60 + now.minute
    active = day.weekday() < 5 and (570 <= n < 690 or 780 <= n < 900)
    if day.weekday() >= 5 or n < 570:
        day -= timedelta(days=1)
        while day.weekday() >= 5:
            day -= timedelta(days=1)
        end = 900
    elif n < 690:
        end = 570 + ((n-570)//5+1)*5
    elif n < 780:
        end = 690
    elif n < 900:
        end = 780 + ((n-780)//5+1)*5
    else:
        end = 900
    return day + timedelta(minutes=end), active


class Provider(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def do_GET(self):
        path = urlparse(self.path)
        if path.path == "/daily":
            STATE["daily"] += 1
            self.send_response(503); self.end_headers(); return
        STATE["minute"] += 1
        if STATE["mode"] == "deny":
            self.send_response(403); self.end_headers(); return
        if STATE["mode"] == "timeout":
            time.sleep(2)
            return
        params = parse_qs(path.query)["param"][0].split(",")
        assert params[0] == "sz000001" and params[1] == "m5" and int(params[3]) in (64, 640)
        end, _ = target()
        day = end.replace(hour=0, minute=0, second=0, microsecond=0)
        rows = []
        for start in (570, 780):
            for n in range(start+5, start+121, 5):
                stamp = day + timedelta(minutes=n)
                if stamp <= end:
                    rows.append([stamp.strftime("%Y%m%d%H%M"), "10", "11", "12", "9", "100"])
        raw = json.dumps({"code": 0, "data": {"sz000001": {"m5": rows}}}).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json"); self.send_header("Content-Length", str(len(raw))); self.end_headers(); self.wfile.write(raw)


def main():
    ap=argparse.ArgumentParser(); ap.add_argument("binary",nargs="?",default=str(ROOT/"bin/stock-watcher")); ap.add_argument("--image"); args=ap.parse_args()
    with ThreadingHTTPServer(("127.0.0.1",0),Provider) as provider, tempfile.TemporaryDirectory(prefix="stock-watcher-minute-") as temporary:
        threading.Thread(target=provider.serve_forever,daemon=True).start()
        root=Path(temporary);(root/"data").mkdir();(root/"logs").mkdir()
        with socket.socket() as sock: sock.bind(("127.0.0.1",0));port=sock.getsockname()[1]
        api=f"http://127.0.0.1:{provider.server_port}";url=f"http://127.0.0.1:{port}"
        (root/"config.yaml").write_text(f"""schema_version: 1
data_dir: data
require_cifs: false
request_timeout: 1s
poll_interval: 1h
web:
  enabled: true
  listen: '127.0.0.1:{port}'
intraday:
  enabled: true
  poll_interval: 30s
  tencent_endpoint: {api}/minutes
market:
  providers: [tencent]
  request_interval: 350ms
  tencent_endpoint: {api}/daily
stocks:
  - code: sz000001
    name: 分钟测试
calendar:
  valid_from: '2020-01-01'
  valid_through: '2099-12-31'
  holidays: []
""")
        env={k:v for k,v in os.environ.items() if not k.startswith(("FEISHU_","TUSHARE_","STOCK_WATCHER_"))}
        env.update(STOCK_WATCHER_LOG_DIR=str(root/"logs"),STOCK_WATCHER_LOG_COLOR="off",NO_PROXY="127.0.0.1,localhost")
        name="stock-watcher-minute-test-"+str(os.getpid())
        mount="/fixture" if args.image else str(root)
        def command(verb, service=False):
            if args.image:
                prefix=["docker","run","--rm","--network","host","--read-only","--cpus","1","--memory","256m","--pids-limit","64","--cap-drop","ALL","--security-opt","no-new-privileges:true","--mount",f"type=bind,src={root},dst=/fixture","-e","STOCK_WATCHER_LOG_DIR=/fixture/logs","-e","STOCK_WATCHER_LOG_COLOR=off"]
                if service: prefix += ["--name",name]
                prefix += [args.image]
            else: prefix=[args.binary]
            return prefix+[verb,"-config",mount+"/config.yaml"]
        def invoke(verb, fail=False):
            r=subprocess.run(command(verb),env=env,capture_output=True,text=True,timeout=30)
            assert r.returncode==(1 if fail else 0),r.stdout+r.stderr
            return r
        def reset_attempt():
            with sqlite3.connect(root/"data/market.db") as db: db.execute("UPDATE intraday_sync SET attempt_at='2020-01-01T00:00:00+08:00',calibrated_date=''")
        def saved():
            with sqlite3.connect(root/"data/market.db") as db:
                return db.execute("SELECT end_unix,close_micros,volume_raw FROM intraday_bar ORDER BY end_unix").fetchall()
        try:
            invoke("sync-intraday",fail=True)
            invoke("init")
            # Simulate the previous schema on disk; the normal CLI migrates it.
            with sqlite3.connect(root/"data/market.db") as db: db.executescript("DROP TABLE intraday_bar; DROP TABLE intraday_sync; PRAGMA user_version=2;")
            invoke("sync-intraday")
            before=saved();assert before
            for mode in ("deny","timeout"):
                reset_attempt();STATE["mode"]=mode;invoke("sync-intraday",fail=True);assert saved()==before
            STATE["mode"]="ok";reset_attempt()
            with (root/"stdout.log").open("w") as log:
                proc=subprocess.Popen(command("run",True),env=env,stdout=log,stderr=subprocess.STDOUT)
                try:
                    until=time.monotonic()+15
                    while True:
                        assert proc.poll() is None,(root/"stdout.log").read_text()
                        try:
                            with urlopen(url+"/api/intraday?code=sz000001&period=5",timeout=1) as response: snap=json.load(response)
                            if snap["sync"]["status"]=="ok" and STATE["daily"]>0:break
                        except OSError:pass
                        assert time.monotonic()<until,(root/"stdout.log").read_text()
                        time.sleep(.1)
                    for period in (5,15,30,60):
                        with urlopen(url+f"/api/intraday?code=sz000001&period={period}",timeout=3) as response:
                            data=json.load(response)
                            assert response.headers["Cache-Control"]=="no-store"
                        assert data["base_period_minutes"]==5 and data["period_minutes"]==period
                        if not target()[1] and target()[0].hour==15: assert len(data["bars"])==240//period
                    with sqlite3.connect(root/"data/market.db") as db:
                        assert db.execute("PRAGMA user_version").fetchone()==(3,)
                        assert db.execute("PRAGMA integrity_check").fetchone()==("ok",)
                        assert db.execute("SELECT count(*) FROM daily_bar").fetchone()==(0,)
                finally:
                    if args.image: subprocess.run(["docker","stop","--time","10",name],capture_output=True,timeout=15)
                    else: proc.terminate()
                    proc.wait(timeout=15)
                assert proc.returncode==0,(root/"stdout.log").read_text()
            # Process released the writer lock; read-only and writer commands work again.
            invoke("status");invoke("sync-intraday")
            print(json.dumps({"environment":args.image or "native","url":url,"result":"passed","checks":"schema2 migration/minutes despite daily failure/4 periods/403+timeout preservation/read API/SIGTERM/lock cleanup"}))
        finally:
            provider.shutdown()


if __name__=="__main__":main()
