cmake_minimum_required(VERSION 3.20)
project(L06 LANGUAGES C VERSION 0.1.0)


add_library(L06 STATIC src/L06.c)
target_include_directories(L06 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)

install(TARGETS L06
        EXPORT L06Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L06Targets
        FILE L06Targets.cmake
        NAMESPACE L06::
        DESTINATION lib/cmake/L06)
