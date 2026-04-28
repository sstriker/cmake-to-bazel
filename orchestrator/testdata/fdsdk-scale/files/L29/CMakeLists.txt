cmake_minimum_required(VERSION 3.20)
project(L29 LANGUAGES C VERSION 0.1.0)

find_package(L09 CONFIG REQUIRED)
find_package(L00 CONFIG REQUIRED)

add_library(L29 STATIC src/L29.c)
target_include_directories(L29 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L29 PUBLIC L09::L09 L00::L00)

install(TARGETS L29
        EXPORT L29Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L29Targets
        FILE L29Targets.cmake
        NAMESPACE L29::
        DESTINATION lib/cmake/L29)
