cmake_minimum_required(VERSION 3.20)
project(L10 LANGUAGES C VERSION 0.1.0)

find_package(L00 CONFIG REQUIRED)
find_package(L01 CONFIG REQUIRED)

add_library(L10 STATIC src/L10.c)
target_include_directories(L10 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L10 PUBLIC L00::L00 L01::L01)

install(TARGETS L10
        EXPORT L10Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L10Targets
        FILE L10Targets.cmake
        NAMESPACE L10::
        DESTINATION lib/cmake/L10)
