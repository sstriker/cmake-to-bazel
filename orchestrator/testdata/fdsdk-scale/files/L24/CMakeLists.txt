cmake_minimum_required(VERSION 3.20)
project(L24 LANGUAGES C VERSION 0.1.0)

find_package(L04 CONFIG REQUIRED)
find_package(L05 CONFIG REQUIRED)

add_library(L24 STATIC src/L24.c)
target_include_directories(L24 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L24 PUBLIC L04::L04 L05::L05)

install(TARGETS L24
        EXPORT L24Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L24Targets
        FILE L24Targets.cmake
        NAMESPACE L24::
        DESTINATION lib/cmake/L24)
