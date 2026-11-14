cmake_minimum_required(VERSION 3.20)
project(phone-capi LANGUAGES C CXX)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()
add_compile_options(-Wall -Wextra)

# libphoneui: the C ABI SDK (phoneui.h), static and shared.
add_library(phoneui_objects OBJECT phoneui.c)
set_target_properties(phoneui_objects PROPERTIES POSITION_INDEPENDENT_CODE ON)
add_library(phoneui STATIC $<TARGET_OBJECTS:phoneui_objects>)
add_library(phoneui_shared SHARED $<TARGET_OBJECTS:phoneui_objects>)
set_target_properties(phoneui_shared PROPERTIES OUTPUT_NAME phoneui VERSION 0.1.0 SOVERSION 0)
target_include_directories(phoneui PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})

add_executable(phone-app-hellocpp examples/hello-cpp/main.cpp)
target_link_libraries(phone-app-hellocpp PRIVATE phoneui)

enable_testing()
add_executable(phoneui_test test_phoneui.c)
target_link_libraries(phoneui_test PRIVATE phoneui)
add_test(NAME phoneui_test COMMAND phoneui_test)

install(TARGETS phoneui phoneui_shared ARCHIVE DESTINATION lib LIBRARY DESTINATION lib)
install(FILES phoneui.h DESTINATION include)
install(TARGETS phone-app-hellocpp RUNTIME DESTINATION lib/phone/bin)
# The sample's translations, in its app directory (apps/hello-cpp).
install(DIRECTORY examples/hello-cpp/locale DESTINATION share/phone/apps/hello-cpp)
