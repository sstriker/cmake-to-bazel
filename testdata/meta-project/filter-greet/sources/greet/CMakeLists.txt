cmake_minimum_required(VERSION 3.20)
project(greet LANGUAGES C VERSION 0.1.0)

add_library(greet STATIC greet.c)
target_include_directories(greet PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS greet
        EXPORT greetTargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT greetTargets
        FILE greetTargets.cmake
        NAMESPACE greet::
        DESTINATION lib/cmake/greet)
