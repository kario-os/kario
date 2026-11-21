/* What storage_test.go preloads into a runtime (LD_PRELOAD): fsync takes
 * SLOWFS_DELAY_MS longer, as slow flash does, and each call appends a line
 * to SLOWFS_LOG saying which thread made it ("main" or "worker"). Built by
 * the test (cc -shared). */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdlib.h>
#include <string.h>
#include <sys/syscall.h>
#include <time.h>
#include <unistd.h>

int fsync(int fd)
{
	const char *log = getenv("SLOWFS_LOG");
	if (log && *log) {
		int l = open(log, O_WRONLY | O_CREAT | O_APPEND | O_CLOEXEC, 0644);
		if (l >= 0) {
			const char *who = syscall(SYS_gettid) == getpid() ? "main\n" : "worker\n";
			ssize_t w = write(l, who, strlen(who));
			(void)w;
			close(l);
		}
	}
	const char *d = getenv("SLOWFS_DELAY_MS");
	long ms = d ? atol(d) : 0;
	if (ms > 0) {
		struct timespec ts = {ms / 1000, (ms % 1000) * 1000000L};
		while (nanosleep(&ts, &ts) < 0 && errno == EINTR)
			;
	}
	return (int)syscall(SYS_fsync, fd);
}
