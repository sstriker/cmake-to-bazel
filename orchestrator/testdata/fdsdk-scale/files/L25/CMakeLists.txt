cmake_minimum_required(VERSION 3.20)
project(L25 LANGUAGES C VERSION 0.1.0)

find_package(L05 CONFIG REQUIRED)
find_package(L06 CONFIG REQUIRED)

add_library(L25 STATIC src/L25.c)
target_include_directories(L25 PUBLIC
    $<BUILD_INTERFACE:${CMAKE_CURRENT_SOURCE_DIR}/include>
    $<INSTALL_INTERFACE:include>)
target_link_libraries(L25 PUBLIC L05::L05 L06::L06)

install(TARGETS L25
        EXPORT L25Targets
        ARCHIVE DESTINATION lib)
install(EXPORT L25Targets
        FILE L25Targets.cmake
        NAMESPACE L25::
        DESTINATION lib/cmake/L25)
