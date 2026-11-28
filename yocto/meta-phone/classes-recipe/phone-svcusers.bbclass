# Every service the image starts must run as a user it can run as and the
# system bus takes: its User= and Group= in the image's passwd and group,
# and no DynamicUser= while the bus is dbus-daemon, which closes the
# connection of a user it cannot look up (q6voiced's upstream unit on the
# OnePlus 6T, 39-oneplus-6t.md §14.20). files/phone-svcusers-check.py
# reads the units, their drop-ins and the bus's.
#
#   inherit phone-svcusers

PHONE_SVCUSERS_CHECK = "${@bb.utils.which(d.getVar('BBPATH'), 'files/phone-svcusers-check.py')}"

ERROR_QA:append = " service-users"
do_image_qa[file-checksums] += "${PHONE_SVCUSERS_CHECK}:True"
IMAGE_QA_COMMANDS += "phone_svcusers_check"

python phone_svcusers_check() {
    import subprocess
    r = subprocess.run(['python3', d.getVar('PHONE_SVCUSERS_CHECK'), d.getVar('IMAGE_ROOTFS')],
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    bb.note(r.stdout)
    if r.returncode != 0:
        oe.qa.handle_error("service-users", "services of this image cannot run as their users:\n" + r.stdout, d)
}
