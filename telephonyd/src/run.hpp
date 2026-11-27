// A program telephonyd runs off its loop (the modem's IMS registration,
// ims.hpp). What it writes to its standard output and error, and whether
// it exited 0, go to done once it has exited and its output has ended; a
// program still running at the timeout counts as failed, and done comes
// then, not when what was killed has gone. Either way the run ends with
// the program's process group, what it started; the program and its
// group end with telephonyd, however telephonyd ends.
#pragma once

#include <chrono>
#include <functional>
#include <string>
#include <vector>

#include "loop.hpp"

namespace tel {

using RunDone = std::function<void(bool ok, std::string output)>;

void run_program(Loop& loop, const std::vector<std::string>& argv, std::chrono::milliseconds timeout, RunDone done);

}  // namespace tel
