cmake_minimum_required(VERSION 3.20)
project(phone-telephonyd LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 23)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()
add_compile_options(-Wall -Wextra -Wno-missing-field-initializers)

# The ModemManager backend talks D-Bus through sd-bus. AUTO builds it when
# libsystemd is there (devices, the D-Bus test container); ON makes a missing
# libsystemd an error instead of a daemon that only has the simulated baseband.
set(TEL_MODEMMANAGER AUTO CACHE STRING "Build the ModemManager backend (AUTO, ON, OFF)")

add_library(telephony STATIC src/json.cpp src/loop.cpp src/modem.cpp src/mm_state.cpp src/service.cpp src/server.cpp)
target_include_directories(telephony PUBLIC src)

if(NOT TEL_MODEMMANAGER STREQUAL "OFF")
  find_package(PkgConfig)
  if(PkgConfig_FOUND)
    pkg_check_modules(SYSTEMD IMPORTED_TARGET libsystemd)
  endif()
  if(SYSTEMD_FOUND)
    target_sources(telephony PRIVATE src/mm.cpp)
    target_compile_definitions(telephony PUBLIC TEL_MODEMMANAGER)
    target_link_libraries(telephony PUBLIC PkgConfig::SYSTEMD)
  elseif(TEL_MODEMMANAGER STREQUAL "ON")
    message(FATAL_ERROR "TEL_MODEMMANAGER=ON needs libsystemd (sd-bus)")
  endif()
endif()

add_executable(phone-telephonyd src/main.cpp)
target_link_libraries(phone-telephonyd PRIVATE telephony)

enable_testing()
add_executable(telephonyd_test test/telephonyd_test.cpp)
target_link_libraries(telephonyd_test PRIVATE telephony)
target_compile_definitions(telephonyd_test PRIVATE TEL_SIM_NETWORK="${CMAKE_CURRENT_SOURCE_DIR}/sim-network.json")
add_test(NAME telephonyd_test COMMAND telephonyd_test)

install(TARGETS phone-telephonyd RUNTIME DESTINATION bin)
# The simulated network's texts, where phone-telephonyd looks for them.
install(FILES sim-network.json DESTINATION share/phone)
