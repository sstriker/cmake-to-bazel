cmake_minimum_required(VERSION 3.20)
project(lib-a LANGUAGES C VERSION 0.1.0)

add_library(lib-a STATIC lib-a.c)
target_include_directories(lib-a PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS lib-a
        EXPORT libATargets
        ARCHIVE DESTINATION lib)
install(DIRECTORY include/ DESTINATION include)
install(EXPORT libATargets
        FILE libATargets.cmake
        NAMESPACE lib_a::
        DESTINATION lib/cmake/lib-a)
