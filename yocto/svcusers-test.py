#!/usr/bin/env python3
"""Test files/phone-svcusers-check.py (phone-svcusers.bbclass's image check)
on root file systems made up here, without an image build.

The bus is dbus-daemon as in Kario's images. The check must refuse
q6voiced's upstream unit (DynamicUser=yes: the 6T's image 007, whose
q6voiced the bus never took) and take it with meta-phone's drop-in and
the user q6voiced's recipe adds; refuse a User= missing from passwd, a
later drop-in turning DynamicUser= back on, a DynamicUser= service started
by an enabled socket or by D-Bus; take a masked or a not enabled one, one
whose name is a static user, and any of them with dbus-broker as the bus.
q6voiced's recipe is kario-bsp's: KARIO_BSP_LAYERS names its layers (make
test gives them, tools/sku.py).

    svcusers-test.py
"""
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(HERE), "tools"))
import sku  # noqa: E402

CHECK = os.path.join(HERE, "meta-phone", "files", "phone-svcusers-check.py")
try:
    Q6 = os.path.dirname(sku.layer_file("recipes-bsp", "q6voiced", "q6voiced_0.3.1.bb"))
except sku.Bad as e:
    sys.exit(f"svcusers-test: {e}")

# q6voiced 0.3.1's own unit, as its package installs it.
UPSTREAM = """[Unit]
Description=q6voice call audio routing
Requires=dbus.service
ConditionPathExists=/usr/share/q6voiced/q6voiced.conf

[Service]
Environment=HOME=/
EnvironmentFile=/usr/share/q6voiced/q6voiced.conf
ExecStart=/usr/bin/q6voiced hw:${q6voice_card},${q6voice_device}
KillMode=process
Restart=always
RestartSec=1
DynamicUser=yes
Group=audio

[Install]
WantedBy=multi-user.target
"""

DBUS_DAEMON = """[Unit]
Requires=dbus.socket

[Service]
Type=notify
ExecStart=/usr/bin/dbus-daemon --system --address=systemd: --nofork --nopidfile --systemd-activation --syslog-only
User=messagebus
"""

DBUS_BROKER = """[Service]
Type=notify
ExecStart=/usr/bin/dbus-broker-launch --scope system --audit
"""

PASSWD = "root:x:0:0:root:/root:/bin/sh\nmessagebus:x:999:998::/var/lib/dbus:/bin/false\n"
GROUP = "root:x:0:\naudio:x:29:\nmessagebus:x:998:\n"


def recipe_user():
    """The user q6voiced's recipe adds (USERADD_PARAM's last word)."""
    with open(os.path.join(Q6, "q6voiced_0.3.1.bb"), encoding="utf-8") as f:
        m = re.search(r'^USERADD_PARAM:\$\{PN\} = "([^"]*)"', f.read(), re.M)
    return m.group(1).split()[-1]


class Rootfs:
    def __init__(self, base):
        self.root = os.path.join(base, "rootfs")
        os.makedirs(os.path.join(self.root, "usr/lib/systemd/system"))
        os.makedirs(os.path.join(self.root, "etc/systemd/system/multi-user.target.wants"))
        # merged /usr, as in Kario's images
        os.symlink("usr/lib", os.path.join(self.root, "lib"))
        self.write("etc/passwd", PASSWD)
        self.write("etc/group", GROUP)
        self.write("usr/lib/systemd/system/dbus.service", DBUS_DAEMON)

    def write(self, path, text):
        full = os.path.join(self.root, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w", encoding="utf-8") as f:
            f.write(text)

    def link(self, path, target):
        full = os.path.join(self.root, path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        os.symlink(target, full)

    def unit(self, name, text, wanted_by="multi-user.target"):
        self.write("usr/lib/systemd/system/" + name, text)
        if wanted_by:
            self.link(f"etc/systemd/system/{wanted_by}.wants/{name}", "/usr/lib/systemd/system/" + name)

    def add_user(self, name, uid=995):
        with open(os.path.join(self.root, "etc/passwd"), "a", encoding="utf-8") as f:
            f.write(f"{name}:x:{uid}:{uid}::/:/bin/false\n")

    def q6voiced(self, kario=True):
        self.unit("q6voiced.service", UPSTREAM)
        if kario:
            with open(os.path.join(Q6, "files", "10-kario.conf"), encoding="utf-8") as f:
                self.write("usr/lib/systemd/system/q6voiced.service.d/10-kario.conf", f.read())
            self.add_user(recipe_user())

    def check(self):
        r = subprocess.run([sys.executable, CHECK, self.root], capture_output=True, text=True)
        return r.returncode, r.stdout


class SvcUsersTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="svcusers-test-")
        self.fs = Rootfs(self.tmp)

    def tearDown(self):
        shutil.rmtree(self.tmp)

    def refused(self, *words):
        rc, out = self.fs.check()
        self.assertEqual(rc, 1, out)
        for w in words:
            self.assertIn(w, out)
        return out

    def taken(self):
        rc, out = self.fs.check()
        self.assertEqual(rc, 0, out)

    def test_upstream_q6voiced_refused(self):
        self.fs.q6voiced(kario=False)
        self.refused("q6voiced.service: DynamicUser=", "dbus-daemon")

    def test_q6voiced_with_kario_dropin_taken(self):
        self.fs.q6voiced()
        self.taken()

    def test_user_missing_from_passwd(self):
        self.fs.q6voiced(kario=False)
        with open(os.path.join(Q6, "files", "10-kario.conf"), encoding="utf-8") as f:
            self.fs.write("usr/lib/systemd/system/q6voiced.service.d/10-kario.conf", f.read())
        self.refused(f"q6voiced.service: User={recipe_user()} is not in the image's /etc/passwd")

    def test_group_missing_from_group(self):
        self.fs.unit("radio.service", "[Service]\nExecStart=/bin/true\nUser=messagebus\nGroup=radio\n")
        self.refused("radio.service: Group=radio is not in the image's /etc/group")

    def test_later_dropin_turns_dynamic_user_back_on(self):
        self.fs.q6voiced()
        self.fs.write("etc/systemd/system/q6voiced.service.d/50-local.conf", "[Service]\nDynamicUser=yes\nUser=q6voice-dyn\n")
        self.refused("q6voiced.service: DynamicUser=")

    def test_dropin_resets_dynamic_user(self):
        self.fs.unit("worker.service", UPSTREAM.replace("q6voiced", "worker"))
        self.fs.write("etc/systemd/system/worker.service.d/10-static.conf", "[Service]\nDynamicUser=\nUser=messagebus\n")
        self.taken()

    def test_etc_dropin_replaces_one_of_the_same_name(self):
        self.fs.unit("worker.service", UPSTREAM.replace("q6voiced", "worker"))
        self.fs.write("usr/lib/systemd/system/worker.service.d/10-static.conf", "[Service]\nDynamicUser=no\nUser=messagebus\n")
        self.taken()
        self.fs.write("etc/systemd/system/worker.service.d/10-static.conf", "[Service]\nRestartSec=5\n")
        self.refused("worker.service: DynamicUser=")

    def test_masked_and_not_enabled_taken(self):
        self.fs.q6voiced(kario=False)
        self.fs.link("etc/systemd/system/q6voiced.service", "/dev/null")
        self.fs.unit("capsule@.service", "[Service]\nExecStart=/bin/true\nDynamicUser=yes\n", wanted_by=None)
        self.taken()

    # systemd runs a DynamicUser= service as the static user of its name
    # where there is one (dynamic_user_realize).
    def test_static_user_of_the_name_taken(self):
        self.fs.q6voiced(kario=False)
        self.fs.add_user("q6voiced")
        self.taken()

    def test_started_by_socket(self):
        self.fs.unit("relay.socket", "[Socket]\nListenStream=/run/relay\n", wanted_by="sockets.target")
        self.fs.unit("relay.service", "[Service]\nExecStart=/bin/true\nDynamicUser=yes\n", wanted_by=None)
        self.refused("relay.service: DynamicUser=")

    def test_started_by_socket_accepting(self):
        self.fs.unit("relay.socket", "[Socket]\nListenStream=/run/relay\nAccept=yes\n", wanted_by="sockets.target")
        self.fs.unit("relay@.service", "[Service]\nExecStart=/bin/true\nDynamicUser=yes\n", wanted_by=None)
        self.refused("relay@.service: DynamicUser=")

    def test_started_by_dbus(self):
        self.fs.write("usr/share/dbus-1/system-services/org.example.Relay.service",
                      "[D-BUS Service]\nName=org.example.Relay\nExec=/bin/false\nUser=root\nSystemdService=relay.service\n")
        self.fs.unit("relay.service", "[Service]\nExecStart=/bin/true\nDynamicUser=yes\n", wanted_by=None)
        self.refused("relay.service: DynamicUser=")

    def test_dbus_broker_takes_dynamic_users(self):
        self.fs.q6voiced(kario=False)
        self.fs.unit("dbus-broker.service", DBUS_BROKER, wanted_by=None)
        os.remove(os.path.join(self.fs.root, "usr/lib/systemd/system/dbus.service"))
        self.fs.link("usr/lib/systemd/system/dbus.service", "dbus-broker.service")
        self.taken()


if __name__ == "__main__":
    unittest.main()
