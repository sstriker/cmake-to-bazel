cmake_minimum_required(VERSION 3.20)
project(L45 LANGUAGES C VERSION 0.1.0)

find_package(L30 CONFIG REQUIRED)
find_package(L35 CONFIG REQUIRED)
find_package(L37 CONFIG REQUIRED)

add_library(L45 STATIC src/L45.c)
target_include_directories(L45 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L45 PUBLIC L30::L30 L35::L35 L37::L37)

install(TARGETS L45
        EXPORT L45Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L45Targets
        FILE L45Targets.cmake
        NAMESPACE L45::
        DESTINATION lib/cmake/L45)
