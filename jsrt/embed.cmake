# A text file as a C array of its bytes and a NUL, for the runtime to
# compile (CMakeLists.txt): cmake -DIN=file -DOUT=header -DNAME=symbol -P embed.cmake
file(READ "${IN}" hex HEX)
string(REGEX REPLACE "([0-9a-f][0-9a-f])" "0x\\1," hex "${hex}")
get_filename_component(base "${IN}" NAME)
file(WRITE "${OUT}" "/* ${base}, written by jsrt/embed.cmake. */\nstatic const char ${NAME}[] = {${hex}0};\n")
