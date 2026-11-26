// Single-threaded event loop: readable file descriptors and timers. All of
// telephonyd runs on it, so no state needs locking.
#pragma once

#include <chrono>
#include <cstdint>
#include <functional>
#include <map>
#include <queue>
#include <vector>

namespace tel {

class Loop {
public:
    using Clock = std::chrono::steady_clock;
    using Task = std::function<void()>;

    void watch(int fd, Task on_readable) { fds_[fd] = std::move(on_readable); }
    void unwatch(int fd) { fds_.erase(fd); }
    void after(std::chrono::milliseconds delay, Task task);
    void run();
    void stop() { stopped_ = true; }

    Clock::time_point now() const { return Clock::now() + skew_; }
    // Tests: move the clock forward and run whatever became due.
    void advance(std::chrono::milliseconds delay);

private:
    struct Timer {
        Clock::time_point at;
        std::uint64_t seq;
        Task task;
        bool operator>(const Timer& o) const { return at != o.at ? at > o.at : seq > o.seq; }
    };
    void run_due();

    std::priority_queue<Timer, std::vector<Timer>, std::greater<>> timers_;
    std::map<int, Task> fds_;
    std::uint64_t seq_ = 0;
    Clock::duration skew_{};
    bool stopped_ = false;
};

}  // namespace tel
