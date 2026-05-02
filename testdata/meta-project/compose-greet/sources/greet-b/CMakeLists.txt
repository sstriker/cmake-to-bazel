cmake_minimum_required(VERSION 3.20)
project(greet-b LANGUAGES C VERSION 0.1.0)

add_library(greet-b STATIC greet-b.c)
target_include_directories(greet-b PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS greet-b
        EXPORT greetBTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT greetBTargets
        FILE greetBTargets.cmake
        NAMESPACE greet_b::
        DESTINATION lib/cmake/greet-b)
