#!/usr/bin/env python3
"""Exercise the running demo API and worker. Refuses to touch live mode."""
import http.cookiejar
import json
import time
import urllib.request

BASE = "http://127.0.0.1:8080/api"
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

def request(path, method="GET", data=None):
    body = None if data is None else json.dumps(data).encode()
    req = urllib.request.Request(BASE + path, body, {"Content-Type": "application/json", "X-VDome-Request": "1"}, method=method)
    with opener.open(req, timeout=15) as response:
        return json.load(response)

def wait_for(check):
    deadline = time.monotonic() + 12
    while time.monotonic() < deadline:
        state = request("/state")
        if check(state):
            return state
        time.sleep(.25)
    raise AssertionError("worker did not reach expected state")

assert request("/state")["provider"] == "demo", "Refusing to run against a real provider"
challenge = request("/auth/login", "POST", {"phone": "+70000000001"})["challenge"]
request("/auth/login", "POST", {"phone": "+70000000001", "code": "000000", "challenge": challenge})
try:
    wait_for(lambda s: s["connected"])
    request("/devices/entrance/mode", "PUT", {"mode": "next"})
    request("/devices/entrance/simulate-call", "POST")
    state = wait_for(lambda s: len(s["audit"]) == 1)
    assert state["rules"]["entrance"]["mode"] == "off"
    assert state["audit"][0]["result"] == "accepted"
    print("PASS: login, devices, background worker, one-shot consumption")
    with opener.open(BASE + "/devices/entrance/snapshot") as response:
        assert response.headers.get_content_type() == "image/svg+xml"
        assert b"<svg" in response.read()
    print("PASS: authenticated camera snapshot")
    time.sleep(10.1)
    request("/devices/entrance/mode", "PUT", {"mode": "always"})
    request("/devices/entrance/simulate-call", "POST")
    state = wait_for(lambda s: len(s["audit"]) == 2)
    assert state["rules"]["entrance"]["mode"] == "always"
    print("PASS: always mode remains armed after a call")
finally:
    request("/auth/logout", "POST")
assert not request("/state")["authenticated"]
print("PASS: logout revokes session and automation")
