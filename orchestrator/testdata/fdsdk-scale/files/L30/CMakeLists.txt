cmake_minimum_required(VERSION 3.20)
project(L30 LANGUAGES C VERSION 0.1.0)

find_package(L10 CONFIG REQUIRED)
find_package(L13 CONFIG REQUIRED)

add_library(L30 STATIC src/L30.c)
target_include_directories(L30 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L30 PUBLIC L10::L10 L13::L13)

install(TARGETS L30
        EXPORT L30Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L30Targets
        FILE L30Targets.cmake
        NAMESPACE L30::
        DESTINATION lib/cmake/L30)
