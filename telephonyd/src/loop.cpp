#include "loop.hpp"

#include <poll.h>

#include <cerrno>

namespace tel {

void Loop::after(std::chrono::milliseconds delay, Task task) {
    timers_.push(Timer{now() + delay, seq_++, std::move(task)});
}

void Loop::run_due() {
    while (!timers_.empty() && timers_.top().at <= now()) {
        Task t = timers_.top().task;
        timers_.pop();
        t();
    }
}

void Loop::advance(std::chrono::milliseconds delay) {
    skew_ += delay;
    run_due();
}

void Loop::run() {
    while (!stopped_) {
        run_due();
        int timeout = -1;
        if (!timers_.empty()) {
            auto wait = std::chrono::duration_cast<std::chrono::milliseconds>(timers_.top().at - now()).count();
            timeout = static_cast<int>(std::max<long long>(0, wait));
        }
        std::vector<pollfd> pfds;
        for (const auto& [fd, _] : fds_) pfds.push_back(pollfd{fd, POLLIN, 0});
        int n = ::poll(pfds.data(), pfds.size(), timeout);
        if (n < 0 && errno != EINTR) return;
        for (const auto& p : pfds) {
            // A handler may unwatch other descriptors; look each one up again.
            if (p.revents == 0) continue;
            if (auto it = fds_.find(p.fd); it != fds_.end()) {
                Task t = it->second;
                t();
            }
        }
    }
}

}  // namespace tel
