#include "run.hpp"

#include <fcntl.h>
#include <signal.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>

#include <algorithm>
#include <cerrno>
#include <cstring>
#include <format>
#include <memory>

#ifndef SYS_close_range
#define SYS_close_range 436
#endif

extern char** environ;

namespace tel {

using namespace std::chrono_literals;

namespace {

// Enough for a word and a line of why; the rest is dropped.
constexpr size_t kMaxOutput = 4096;

struct Child {
    pid_t pid = -1, guard = -1;
    int fd = -1;
    bool finished = false;
    std::string out;
    RunDone done;
};

// telephonyd has no SIGCHLD handler: what it started is polled for.
void reap(Loop& loop, pid_t pid) {
    if (pid > 0 && ::waitpid(pid, nullptr, WNOHANG) == 0) loop.after(20ms, [&loop, pid] { reap(loop, pid); });
}

// The run's end, at the program's exit or the timeout: its process group
// (what it started, the guard) is killed while the unreaped program still
// holds the group's id, and done comes without waiting for any of it to go.
void finish(Loop& loop, const std::shared_ptr<Child>& c, bool ok, std::string out) {
    c->finished = true;
    ::kill(-c->pid, SIGKILL);
    if (c->fd >= 0) {
        loop.unwatch(c->fd);
        ::close(c->fd);
        c->fd = -1;
    }
    reap(loop, c->pid);
    reap(loop, c->guard);
    c->done(ok, std::move(out));
}

// After the output's end: the program's exit, seen without reaping it.
void exited(Loop& loop, const std::shared_ptr<Child>& c) {
    if (c->finished) return;
    siginfo_t si{};
    int r = ::waitid(P_PID, c->pid, &si, WEXITED | WNOHANG | WNOWAIT);
    if (r == 0 && si.si_pid == 0) {
        loop.after(20ms, [&loop, c] { exited(loop, c); });
        return;
    }
    finish(loop, c, r == 0 && si.si_code == CLD_EXITED && si.si_status == 0, std::move(c->out));
}

// The guard, a second child in the program's process group: the
// parent-death signal ends the program alone, not what it started (the
// IMS status script's qmicli).
[[noreturn]] void guard(pid_t parent, pid_t group) {
    ::setpgid(0, group);
    if (::getpgrp() != group) ::_exit(0);
    // It never execs: telephonyd's sockets would stay open in it.
    if (::syscall(SYS_close_range, 3u, ~0u, 0) != 0)
        for (long fd = 3, n = ::sysconf(_SC_OPEN_MAX); fd < n; ++fd) ::close(static_cast<int>(fd));
    sigset_t set;
    ::sigemptyset(&set);
    ::sigaddset(&set, SIGHUP);
    ::signal(SIGHUP, SIG_DFL);
    ::sigprocmask(SIG_BLOCK, &set, nullptr);
    ::prctl(PR_SET_PDEATHSIG, SIGHUP);
    for (int sig; ::getppid() == parent;) ::sigwait(&set, &sig);
    ::kill(-group, SIGKILL);
    ::_exit(0);
}

}  // namespace

void run_program(Loop& loop, const std::vector<std::string>& argv, std::chrono::milliseconds timeout, RunDone done) {
    auto fail = [&loop, &done](std::string why) { loop.after(0ms, [done = std::move(done), why = std::move(why)] { done(false, why); }); };
    int p[2];
    if (argv.empty()) return fail("no program");
    if (::pipe2(p, O_CLOEXEC) < 0) return fail(std::format("pipe: {}", std::strerror(errno)));
    ::fcntl(p[0], F_SETFL, O_NONBLOCK);

    std::vector<char*> args;
    for (const auto& a : argv) args.push_back(const_cast<char*>(a.c_str()));
    args.push_back(nullptr);
    std::string cannot = argv[0] + ": ";
    // fork, for the parent-death signal posix_spawn cannot set: the program
    // ends with telephonyd, where a supervisor other than systemd restarts
    // it (phone-shell --services spawn) and the timeout that would kill a
    // program that hangs went with the old telephonyd. Linux sends the
    // signal when the forking thread ends: telephonyd has only the one.
    pid_t parent = ::getpid();
    pid_t pid = ::fork();
    if (pid == 0) {
        ::prctl(PR_SET_PDEATHSIG, SIGKILL);
        if (::getppid() != parent) ::_exit(127);
        ::setpgid(0, 0);
        // telephonyd ignores SIGPIPE, which a program would inherit.
        ::signal(SIGPIPE, SIG_DFL);
        int null = ::open("/dev/null", O_RDONLY);
        ::dup2(null, 0);
        ::dup2(p[1], 1);
        ::dup2(p[1], 2);
        for (int fd : {0, 1, 2}) ::fcntl(fd, F_SETFD, 0);
        if (null > 2) ::close(null);
        ::execve(args[0], args.data(), environ);
        cannot += std::strerror(errno);
        [[maybe_unused]] auto n = ::write(2, cannot.data(), cannot.size());
        ::_exit(127);
    }
    int forked = errno;
    ::close(p[1]);
    if (pid < 0) {
        ::close(p[0]);
        return fail(std::format("{}: {}", argv[0], std::strerror(forked)));
    }
    ::setpgid(pid, pid);
    // Without a guard the program still ends with telephonyd and at the
    // timeout with what it started.
    pid_t g = ::fork();
    if (g == 0) guard(parent, pid);
    if (g > 0) ::setpgid(g, pid);

    auto c = std::make_shared<Child>(Child{.pid = pid, .guard = g, .fd = p[0], .done = std::move(done)});
    loop.watch(c->fd, [&loop, c] {
        char buf[512];
        for (;;) {
            ssize_t n = ::read(c->fd, buf, sizeof buf);
            if (n > 0) {
                c->out.append(buf, std::min(static_cast<size_t>(n), kMaxOutput - std::min(kMaxOutput, c->out.size())));
                continue;
            }
            if (n < 0 && errno == EINTR) continue;
            if (n < 0 && errno == EAGAIN) return;
            break;
        }
        loop.unwatch(c->fd);
        ::close(c->fd);
        c->fd = -1;
        exited(loop, c);
    });
    loop.after(timeout, [&loop, c] {
        if (!c->finished) finish(loop, c, false, "it did not finish in time");
    });
}

}  // namespace tel
