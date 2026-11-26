/* What storage_test.go preloads into a runtime (LD_PRELOAD): fsync takes
 * SLOWFS_DELAY_MS longer, as slow flash does, and first waits as long as
 * the file SLOWFS_GATE exists (a test holds a write under way until it
 * lets it go). Each fsync and rename, once done, appends a line to
 * SLOWFS_LOG: the thread that made it ("main" or "worker") and the call
 * ("fsync" of a file, "dirsync" of a directory, "rename"). Built by the
 * test (cc -shared). */
#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <time.h>
#include <unistd.h>

static void note(const char *call)
{
	const char *log = getenv("SLOWFS_LOG");
	if (!log || !*log)
		return;
	char line[32];
	int n = snprintf(line, sizeof line, "%s %s\n", syscall(SYS_gettid) == getpid() ? "main" : "worker", call);
	int l = open(log, O_WRONLY | O_CREAT | O_APPEND | O_CLOEXEC, 0644);
	if (l >= 0) {
		ssize_t w = write(l, line, (size_t)n);
		(void)w;
		close(l);
	}
}

int fsync(int fd)
{
	const char *gate = getenv("SLOWFS_GATE");
	struct timespec tick = {0, 5 * 1000000L};
	while (gate && *gate && access(gate, F_OK) == 0)
		nanosleep(&tick, NULL);
	const char *d = getenv("SLOWFS_DELAY_MS");
	long ms = d ? atol(d) : 0;
	if (ms > 0) {
		struct timespec ts = {ms / 1000, (ms % 1000) * 1000000L};
		while (nanosleep(&ts, &ts) < 0 && errno == EINTR)
			;
	}
	struct stat st;
	int dir = fstat(fd, &st) == 0 && S_ISDIR(st.st_mode);
	int ret = (int)syscall(SYS_fsync, fd);
	int saved = errno;
	note(dir ? "dirsync" : "fsync");
	errno = saved;
	return ret;
}

int rename(const char *from, const char *to)
{
	int ret = (int)syscall(SYS_renameat2, AT_FDCWD, from, AT_FDCWD, to, 0);
	int saved = errno;
	note("rename");
	errno = saved;
	return ret;
}
