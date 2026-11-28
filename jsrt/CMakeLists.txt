cmake_minimum_required(VERSION 3.20)
project(kario-jsrt LANGUAGES C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

# QuickJS-ng: the release `make jsrt` fetched, checked and patched (QJS_SRC;
# kario-debugger.patch, the debugger's hooks), or the one installed in the
# sysroot (the quickjs recipe on Yocto, with the same patch). Only the
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

add_executable(kario-jsrt src/main.c src/kario.c src/dispatch.c src/files.c src/web.c src/utf8dec.c src/lang.c src/native.c src/store.c src/debug.c ${PHONEUI_SRC}/phoneui.c)
target_include_directories(kario-jsrt PRIVATE src ${PHONEUI_SRC} ${PHONEUI_SRC}/include)
target_compile_options(kario-jsrt PRIVATE -Wall -Wextra -Wno-unused-parameter -ffunction-sections -fdata-sections)
target_link_options(kario-jsrt PRIVATE -Wl,--gc-sections)
# Native modules (kario_native.h) are dlopen'ed and their asynchronous
# functions run on worker threads; the storage file is written on one.
find_package(Threads REQUIRED)
target_link_libraries(kario-jsrt PRIVATE qjs m ${CMAKE_DL_LIBS} Threads::Threads)

# fetch (src/net.c, the response's headers in src/fetchhead.c) and
# WebSocket (src/ws.c, framing in src/wsframe.c) go through libcurl; the
# build host's runtime, which only compiles bytecode, goes without (the
# module then has neither).
option(KARIO_JSRT_NET "fetch and WebSocket through libcurl" ON)
if(KARIO_JSRT_NET)
  find_package(CURL REQUIRED)
  target_sources(kario-jsrt PRIVATE src/net.c src/fetchhead.c src/ws.c src/wsframe.c)
  target_compile_definitions(kario-jsrt PRIVATE KARIO_JSRT_NET=1)
  target_link_libraries(kario-jsrt PRIVATE CURL::libcurl)
  # The web's Blob, File, FormData, Headers, Request and Response and the
  # fetch taking them: the JavaScript Node.js runs too, in the runtime as
  # text that src/net.c compiles once an app uses them.
  set(WEB_JS "${CMAKE_CURRENT_SOURCE_DIR}/../sdk/ts/node/web.js")
  add_custom_command(OUTPUT ${CMAKE_CURRENT_BINARY_DIR}/web_js.h
    COMMAND ${CMAKE_COMMAND} -DIN=${WEB_JS} -DOUT=${CMAKE_CURRENT_BINARY_DIR}/web_js.h -DNAME=jsrt_web_js -P ${CMAKE_CURRENT_SOURCE_DIR}/embed.cmake
    DEPENDS ${WEB_JS} ${CMAKE_CURRENT_SOURCE_DIR}/embed.cmake VERBATIM)
  target_sources(kario-jsrt PRIVATE ${CMAKE_CURRENT_BINARY_DIR}/web_js.h)
  target_include_directories(kario-jsrt PRIVATE ${CMAKE_CURRENT_BINARY_DIR})
endif()

install(TARGETS kario-jsrt RUNTIME DESTINATION lib/phone/bin)
