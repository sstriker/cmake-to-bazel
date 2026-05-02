cmake_minimum_required(VERSION 3.20)
project(greet-a LANGUAGES C VERSION 0.1.0)

add_library(greet-a STATIC greet-a.c)
target_include_directories(greet-a PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS greet-a
        EXPORT greetATargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT greetATargets
        FILE greetATargets.cmake
        NAMESPACE greet_a::
        DESTINATION lib/cmake/greet-a)
