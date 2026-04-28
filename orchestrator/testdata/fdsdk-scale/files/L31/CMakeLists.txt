cmake_minimum_required(VERSION 3.20)
project(L31 LANGUAGES C VERSION 0.1.0)

find_package(L11 CONFIG REQUIRED)
find_package(L14 CONFIG REQUIRED)

add_library(L31 STATIC src/L31.c)
target_include_directories(L31 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L31 PUBLIC L11::L11 L14::L14)

install(TARGETS L31
        EXPORT L31Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L31Targets
        FILE L31Targets.cmake
        NAMESPACE L31::
        DESTINATION lib/cmake/L31)
