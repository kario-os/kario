cmake_minimum_required(VERSION 3.20)
project(kario-jsrt LANGUAGES C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

# QuickJS-ng: the release `make jsrt` fetched and checked (QJS_SRC), or the
# one installed in the sysroot (the quickjs recipe on Yocto). Only the
# engine is linked: its std and os modules (quickjs-libc) stay out.
set(QJS_SRC "" CACHE PATH "QuickJS-ng source tree; empty: an installed qjs")
if(QJS_SRC)
  set(QJS_ENABLE_INSTALL OFF CACHE BOOL "" FORCE)
  set(QJS_BUILD_LIBC OFF CACHE BOOL "" FORCE)
  add_subdirectory(${QJS_SRC} quickjs EXCLUDE_FROM_ALL)
  target_compile_options(qjs PRIVATE -ffunction-sections -fdata-sections)
else()
  find_package(qjs CONFIG REQUIRED)
endif()

# The protocol goes through the C interface, compiled in from sdk/c.
set(PHONEUI_SRC "${CMAKE_CURRENT_SOURCE_DIR}/../sdk/c" CACHE PATH "sdk/c (phoneui.h, phoneui.c)")

add_executable(kario-jsrt src/main.c src/kario.c src/dispatch.c src/lang.c ${PHONEUI_SRC}/phoneui.c)
target_include_directories(kario-jsrt PRIVATE src ${PHONEUI_SRC})
target_compile_options(kario-jsrt PRIVATE -Wall -Wextra -Wno-unused-parameter -ffunction-sections -fdata-sections)
target_link_options(kario-jsrt PRIVATE -Wl,--gc-sections)
target_link_libraries(kario-jsrt PRIVATE qjs m)

install(TARGETS kario-jsrt RUNTIME DESTINATION lib/phone/bin)
