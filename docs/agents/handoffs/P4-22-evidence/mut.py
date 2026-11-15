#!/usr/bin/env python3
# Single-site mutation runner. Usage: mut.py <evidence_dir>
import subprocess, sys, os, pathlib
W = os.path.expanduser("~/mc-mut-claude")
EV = sys.argv[1]
url = [l.split("=",1)[1].strip() for l in open(os.path.join(W,".env")) if l.startswith("MINICLOUD_TEST_DATABASE_URL=")][0]
M = [
 ("M1","internal/coordinator/assignment_loop.go",'slog.Warn("assignment pass failed", logsafe.Err(err))','slog.Warn("assignment pass failed", "err", err)',
  "./internal/coordinator/","^TestLogSafe_AssignmentPassFailed_LogsClassOnly$",False),
 ("M1e","internal/coordinator/assignment_loop.go",'slog.Warn("assignment pass failed", logsafe.Err(err))','slog.Warn("assignment pass failed", "err", err)',
  "./tests/faultinject/","^TestFault_DBOutage_RoundRobin_CoordinatorLogsNoDBDetail$",True),
 ("M2","internal/coordinator/assignment.go",'slog.Warn("assignment pass refresh failed", logsafe.Err(err))','slog.Warn("assignment pass refresh failed", "error", err)',
  "./internal/coordinator/","^TestLogSafe_AssignmentPassRefreshFailed_LogsClassOnly$",False),
 ("M3","internal/coordinator/sweeper.go",'slog.Warn("lease sweeper tick failed", logsafe.Err(err))','slog.Warn("lease sweeper tick failed", "err", err)',
  "./internal/coordinator/","^TestLogSafe_LeaseSweeperTickFailed_LogsClassOnly$",False),
 ("M3e","internal/coordinator/sweeper.go",'slog.Warn("lease sweeper tick failed", logsafe.Err(err))','slog.Warn("lease sweeper tick failed", "err", err)',
  "./tests/faultinject/","^TestFault_DBOutage_RoundRobin_CoordinatorLogsNoDBDetail$",True),
 ("M4","internal/api/handlers.go",'slog.Error("database outage or connection failure", logsafe.Err(err))','slog.Error("database outage or connection failure", "err", err)',
  "./internal/api/","^TestLogSafe_APISites_LogClassOnly_StatusUnchanged$",False),
 ("M5","internal/api/handlers.go",'slog.Error("readiness probe failed", logsafe.Err(err))','slog.Error("readiness probe failed", "err", err)',
  "./internal/api/","^TestLogSafe_APISites_LogClassOnly_StatusUnchanged$",False),
 ("M6","internal/api/handlers.go",'slog.Error("internal server error", logsafe.Err(err))','slog.Error("internal server error", "err", err)',
  "./internal/api/","^TestLogSafe_APISites_LogClassOnly_StatusUnchanged$",False),
 ("M7","cmd/coordinator/main.go",'slog.Error("failed to ping postgres database", logsafe.Err(err))','slog.Error("failed to ping postgres database", "err", err)',
  "./tests/faultinject/","^TestFault_CoordinatorStartup_DBUnreachable_LogsNoDBDetail$",False),
 ("M8","cmd/coordinator/main.go",'slog.Error("failed to parse database configuration", logsafe.Err(err))','slog.Error("failed to parse database configuration", "err", err)',
  "./tests/faultinject/","^TestFault_CoordinatorStartup_DBUnreachable_LogsNoDBDetail$",False),
 ("M9","internal/logsafe/logsafe.go",'	return ClassUnclassified\n}','	return err.Error()\n}',
  "./internal/logsafe/","^TestLogsafe_Class_",False),
 ("M10","internal/logsafe/logsafe.go",'	if len(code) != 5 {\n		return "sqlstate_invalid"\n	}','	return "sqlstate_" + code\n	if len(code) != 5 {\n		return "sqlstate_invalid"\n	}',
  "./internal/logsafe/","^TestLogsafe_Class_ServerAndDomainErrors$",False),
 ("M11","internal/logsafe/logsafe.go",'	if errors.As(err, &sqlState) {\n		return sqlStateClass(sqlState.SQLState())\n	}','	if errors.As(err, &sqlState) {\n		return sqlStateClass(sqlState.SQLState()) + ":" + err.Error()\n	}',
  "./internal/logsafe/","^TestLogsafe_Class_",False),
]
only = sys.argv[2:] 
def run(cmd, env, log):
    with open(log,"w") as f:
        f.write("$ "+" ".join(cmd)+"\n"); f.flush()
        r = subprocess.run(cmd, cwd=W, env=env, stdout=f, stderr=subprocess.STDOUT)
        f.write(f"rc={r.returncode}\n")
    return r.returncode
for mid,path,old,new,pkg,pat,needdb in M:
    if only and mid not in only: continue
    subprocess.run(["git","checkout","--",path],cwd=W,check=True)
    p=os.path.join(W,path); s=open(p).read()
    assert s.count(old)==1,(mid,s.count(old))
    s2=s.replace(old,new)
    if "logsafe." not in s2 and "internal/logsafe\"" in s2:
        s2 += "\n// mutation: keep the import used\nvar _ = logsafe.Err\n"
    open(p,"w").write(s2)
    diff=subprocess.run(["git","diff","--",path],cwd=W,capture_output=True,text=True).stdout
    open(os.path.join(EV,f"mut-{mid}.diff"),"w").write(diff)
    env=dict(os.environ); env.pop("MINICLOUD_TEST_DATABASE_URL",None)
    if needdb: env["MINICLOUD_TEST_DATABASE_URL"]=url
    cmd=["go","test","-count=1","-v","-run",pat,pkg]
    red=run(cmd,env,os.path.join(EV,f"mut-{mid}-red.log"))
    subprocess.run(["git","checkout","--",path],cwd=W,check=True)
    green=run(cmd,env,os.path.join(EV,f"mut-{mid}-green.log"))
    print(mid,"red rc",red,"green rc",green, flush=True)
